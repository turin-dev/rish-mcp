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
        versionCode = 1
        versionName = "0.1.0"
    }

    buildFeatures {
        buildConfig = true
    }

    // Sideloaded personal app — skip the release lint gate (see before/app for
    // the original rationale; it also tries to auto-install SDK bits into a
    // read-only image SDK dir under the Docker build).
    lint {
        checkReleaseBuilds = false
        abortOnError = false
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

    // On-device ADB client (pairing + connect + shell), replacing Shizuku.
    // See docs/DESIGN.md §2.1 and §3.1.
    implementation("com.github.MuntashirAkon:libadb-android:3.1.1")
    // Self-signed X.509 cert generation for the ADB auth key pair (no AOSP
    // sun.security.x509 classes on Android otherwise); used by AdbShellClient.
    implementation("com.github.MuntashirAkon:sun-security-android:1.1")

    testImplementation("junit:junit:4.13.2")
}
