buildscript {
    repositories {
        google()
        mavenCentral()
    }
    dependencies {
        // AGP 9's built-in Kotlin pulls Kotlin Gradle plugin 2.2.10 unless a
        // higher version is already on the buildscript classpath. 2.4.21 matches
        // the Kotlin bump and can read OkHttp 5's metadata.
        classpath("org.jetbrains.kotlin:kotlin-gradle-plugin:2.4.21")
    }
}

plugins {
    id("com.android.application") version "9.5.0" apply false
    // Applied conditionally in app/build.gradle.kts, only once a real
    // google-services.json exists (docs/DESIGN.md §3.2 FCM wake path).
    id("com.google.gms.google-services") version "4.5.0" apply false
}
