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
    // Provisioning UI (MainActivity).
    implementation("androidx.appcompat:appcompat:1.7.0")
    implementation("com.google.android.material:material:1.12.0")
    implementation("androidx.lifecycle:lifecycle-runtime-ktx:2.8.7")

    // On-device ADB client (pairing + connect + shell), replacing Shizuku.
    // See docs/DESIGN.md §2.1 and §3.1.
    implementation("com.github.MuntashirAkon:libadb-android:3.1.1")
    // Self-signed X.509 cert generation for the ADB auth key pair (no AOSP
    // sun.security.x509 classes on Android otherwise); used by AdbShellClient.
    implementation("com.github.MuntashirAkon:sun-security-android:1.1")

    // Low-spec device wake path (docs/DESIGN.md §3.2, roadmap step 4).
    // Harmless to depend on ahead of time: FcmWakeReceiver only does
    // anything once a real google-services.json makes the plugin below
    // active and Firebase actually initializes.
    implementation(platform("com.google.firebase:firebase-bom:34.17.0"))
    implementation("com.google.firebase:firebase-messaging-ktx")

    testImplementation("junit:junit:4.13.2")
}

// Only apply Google Services once a real config file exists, so the build
// doesn't break before a Firebase project is wired up (docs/DESIGN.md §7).
// Swap for `google-services.json.example` locally to see what's expected.
if (file("google-services.json").exists()) {
    apply(plugin = "com.google.gms.google-services")
}
