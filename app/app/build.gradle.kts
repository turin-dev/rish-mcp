plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "kr.scin.rishmcp"
    compileSdk = 35
    buildToolsVersion = "35.0.0"

    defaultConfig {
        applicationId = "kr.scin.rishmcp"
        minSdk = 26
        targetSdk = 35
        versionCode = 10000
        versionName = "1.0.0"
    }

    buildFeatures {
        aidl = true
        buildConfig = true
    }

    // Official signing remains a separate release gate, but debug lint errors
    // must fail local/CI builds.
    lint {
        checkReleaseBuilds = false
        abortOnError = true
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
}

dependencies {
    implementation("androidx.core:core-ktx:1.13.1")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.9.0")
    // Relay WebSocket client (ConnectionManager).
    implementation("com.squareup.okhttp3:okhttp:4.12.0")
    // Provisioning UI (MainActivity).
    implementation("androidx.appcompat:appcompat:1.7.0")
    implementation("com.google.android.material:material:1.12.0")
    implementation("androidx.lifecycle:lifecycle-runtime-ktx:2.8.7")

    // Shell backends. Shizuku is preferred when the owner granted access;
    // on-device ADB remains available as a no-Shizuku fallback.
    implementation("dev.rikka.shizuku:api:13.1.5")
    implementation("dev.rikka.shizuku:provider:13.1.5")
    implementation("com.github.MuntashirAkon:libadb-android:3.1.1")
    // Self-signed X.509 cert generation for the ADB auth key pair (no AOSP
    // sun.security.x509 classes on Android otherwise); used by AdbShellClient.
    implementation("com.github.MuntashirAkon:sun-security-android:1.1")

    testImplementation("junit:junit:4.13.2")
}
