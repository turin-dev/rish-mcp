plugins {
    id("com.android.application") version "9.3.1" apply false
    id("org.jetbrains.kotlin.android") version "2.4.10" apply false
    // Applied conditionally in app/build.gradle.kts, only once a real
    // google-services.json exists (docs/DESIGN.md §3.2 FCM wake path).
    id("com.google.gms.google-services") version "4.5.0" apply false
}
