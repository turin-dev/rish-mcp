package kr.scin.rishmcp

import android.content.Context
import android.os.Build
import android.sun.misc.BASE64Encoder
import android.sun.security.provider.X509Factory
import android.sun.security.x509.AlgorithmId
import android.sun.security.x509.CertificateAlgorithmId
import android.sun.security.x509.CertificateExtensions
import android.sun.security.x509.CertificateIssuerName
import android.sun.security.x509.CertificateSerialNumber
import android.sun.security.x509.CertificateSubjectName
import android.sun.security.x509.CertificateValidity
import android.sun.security.x509.CertificateVersion
import android.sun.security.x509.CertificateX509Key
import android.sun.security.x509.KeyIdentifier
import android.sun.security.x509.PrivateKeyUsageExtension
import android.sun.security.x509.SubjectKeyIdentifierExtension
import android.sun.security.x509.X500Name
import android.sun.security.x509.X509CertImpl
import android.sun.security.x509.X509CertInfo
import io.github.muntashirakon.adb.AbsAdbConnectionManager
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.io.File
import java.io.FileInputStream
import java.io.FileOutputStream
import java.io.IOException
import java.nio.charset.StandardCharsets
import java.security.KeyFactory
import java.security.KeyPairGenerator
import java.security.PrivateKey
import java.security.SecureRandom
import java.security.interfaces.RSAPrivateCrtKey
import java.security.interfaces.RSAPublicKey
import java.security.cert.Certificate
import java.security.cert.CertificateFactory
import java.security.cert.X509Certificate
import java.security.spec.PKCS8EncodedKeySpec
import java.util.Date
import java.util.concurrent.atomic.AtomicBoolean

/**
 * On-device ADB shell client used when Shizuku is unavailable. Wraps
 * libadb-android for wireless-debugging pairing and the standard ADB
 * connect/auth handshake, and layers [ShellV2Protocol] on top of its raw
 * stream API to get separated stdout/stderr and an exit code, which
 * libadb-android's own `shell:` helper does not provide.
 *
 * See docs/DESIGN.md §2.1/§3.1 for the pairing flow this backs (Android 11+
 * wireless pairing now; the pre-11 USB/adb-tcpip path is a later step).
 */
class AdbShellClient private constructor(context: Context) : AbsAdbConnectionManager() {

    private val privateKey: PrivateKey
    private val certificate: Certificate

    init {
        setApi(Build.VERSION.SDK_INT)
        val existingKey = readPrivateKeyFromFile(context)
        val existingCert = readCertificateFromFile(context)
        if (existingKey != null && existingCert != null && keyMatches(existingKey, existingCert)) {
            privateKey = existingKey
            certificate = existingCert
        } else {
            val generated = generateKeyAndCert()
            privateKey = generated.first
            certificate = generated.second
            writePrivateKeyToFile(context, privateKey)
            writeCertificateToFile(context, certificate)
        }
    }

    override fun getPrivateKey(): PrivateKey = privateKey
    override fun getCertificate(): Certificate = certificate
    override fun getDeviceName(): String = "rish-mcp"

    /** Pairs with the device's own wireless-debugging pairing service (Android 11+). */
    suspend fun pairWireless(host: String, port: Int, pairingCode: String): Boolean =
        withContext(Dispatchers.IO) { pair(host, port, pairingCode) }

    /** Connects to the device's own adbd once paired (or, pre-11, tcpip-bridged). */
    suspend fun connectDevice(host: String, port: Int): Boolean =
        withContext(Dispatchers.IO) { connect(host, port) }

    /**
     * Runs [cmd] as a v2 shell command and waits up to [timeoutMs] for it to
     * finish. On timeout the stream is force-closed (mirrors the old agent's
     * "force-kill the process at timeoutMs" behavior) and the result reports
     * exit code -1, matching the run_shell contract in docs/DESIGN.md §3.3.
     */
    suspend fun exec(
        cmd: String,
        timeoutMs: Long,
        maxOutputBytes: Int = DEFAULT_MAX_OUTPUT_BYTES,
    ): ShellResult = withContext(Dispatchers.IO) {
        check(isConnected) { "not connected to an ADB daemon" }
        val start = System.currentTimeMillis()
        val stream = openStream(ShellV2Protocol.destination(cmd))
        val timedOut = AtomicBoolean(false)
        val killer = launch {
            delay(timeoutMs)
            timedOut.set(true)
            runCatching { stream.close() }
        }
        try {
            val parsed = ShellV2Protocol.readResult(stream.openInputStream(), maxOutputBytes)
            ShellResult(
                code = parsed.code,
                stdout = parsed.stdout,
                stderr = parsed.stderr,
                truncated = parsed.truncated,
                durationMs = System.currentTimeMillis() - start,
            )
        } catch (e: IOException) {
            if (timedOut.get()) {
                ShellResult(
                    code = -1,
                    stdout = "",
                    stderr = "",
                    truncated = false,
                    durationMs = System.currentTimeMillis() - start,
                )
            } else {
                throw e
            }
        } finally {
            killer.cancel()
            runCatching { stream.close() }
        }
    }

    companion object {
        // Matches the old Shizuku agent's per-stream cap (before/docs/USAGE.md).
        private const val DEFAULT_MAX_OUTPUT_BYTES = 256 * 1024

        @Volatile
        private var instance: AdbShellClient? = null

        fun getInstance(context: Context): AdbShellClient =
            instance ?: synchronized(this) {
                instance ?: AdbShellClient(context.applicationContext).also { instance = it }
            }

        // --- key/cert file persistence (ported from libadb-android's sample
        // AdbConnectionManager.java; see docs/DESIGN.md §3.1) ---

        private fun readPrivateKeyFromFile(context: Context): PrivateKey? = runCatching {
            val file = File(context.filesDir, "adb_private.key")
            if (!file.exists()) return@runCatching null
            val bytes = file.readBytes()
            val keyFactory = KeyFactory.getInstance("RSA")
            keyFactory.generatePrivate(PKCS8EncodedKeySpec(bytes))
        }.getOrNull()

        private fun writePrivateKeyToFile(context: Context, key: PrivateKey) {
            File(context.filesDir, "adb_private.key").writeBytes(key.encoded)
        }

        private fun readCertificateFromFile(context: Context): Certificate? = runCatching {
            val file = File(context.filesDir, "adb_cert.pem")
            if (!file.exists()) return@runCatching null
            val certificate = FileInputStream(file).use {
                CertificateFactory.getInstance("X.509").generateCertificate(it)
            }
            (certificate as? X509Certificate)?.checkValidity()
            certificate
        }.getOrNull()

        private fun writeCertificateToFile(context: Context, certificate: Certificate) {
            val file = File(context.filesDir, "adb_cert.pem")
            val encoder = BASE64Encoder()
            FileOutputStream(file).use { os ->
                os.write(X509Factory.BEGIN_CERT.toByteArray(StandardCharsets.UTF_8))
                os.write('\n'.code)
                encoder.encode(certificate.encoded, os)
                os.write('\n'.code)
                os.write(X509Factory.END_CERT.toByteArray(StandardCharsets.UTF_8))
            }
        }

        private fun keyMatches(key: PrivateKey, certificate: Certificate): Boolean {
            val privateRsa = key as? RSAPrivateCrtKey ?: return false
            val publicRsa = certificate.publicKey as? RSAPublicKey ?: return false
            return privateRsa.modulus == publicRsa.modulus &&
                privateRsa.publicExponent == publicRsa.publicExponent
        }

        private fun generateKeyAndCert(): Pair<PrivateKey, Certificate> {
            val keyPairGenerator = KeyPairGenerator.getInstance("RSA")
            keyPairGenerator.initialize(2048, SecureRandom())
            val keyPair = keyPairGenerator.generateKeyPair()
            val publicKey = keyPair.public
            val privateKey = keyPair.private

            val algorithmName = "SHA512withRSA"
            val notBefore = Date()
            val notAfter = Date(System.currentTimeMillis() + CERT_VALIDITY_MS)
            val x500Name = X500Name("CN=rish-mcp")

            val extensions = CertificateExtensions()
            extensions.set(
                "SubjectKeyIdentifier",
                SubjectKeyIdentifierExtension(KeyIdentifier(publicKey).identifier),
            )
            extensions.set("PrivateKeyUsage", PrivateKeyUsageExtension(notBefore, notAfter))

            val certInfo = X509CertInfo()
            certInfo.set("version", CertificateVersion(2))
            certInfo.set("serialNumber", CertificateSerialNumber(SecureRandom().nextInt() and Int.MAX_VALUE))
            certInfo.set("algorithmID", CertificateAlgorithmId(AlgorithmId.get(algorithmName)))
            certInfo.set("subject", CertificateSubjectName(x500Name))
            certInfo.set("key", CertificateX509Key(publicKey))
            certInfo.set("validity", CertificateValidity(notBefore, notAfter))
            certInfo.set("issuer", CertificateIssuerName(x500Name))
            certInfo.set("extensions", extensions)

            val certImpl = X509CertImpl(certInfo)
            certImpl.sign(privateKey, algorithmName)
            return privateKey to certImpl
        }

        private const val CERT_VALIDITY_MS = 10L * 365 * 24 * 60 * 60 * 1000
    }
}

/** Mirrors the Go relay's Result struct field-for-field (server/internal/relay). */
data class ShellResult(
    val code: Int,
    val stdout: String,
    val stderr: String,
    val truncated: Boolean,
    val durationMs: Long,
) {
    companion object {
        fun unavailable(detail: String) = ShellResult(
            code = -1,
            stdout = "",
            stderr = detail,
            truncated = false,
            durationMs = 0,
        )
    }
}
