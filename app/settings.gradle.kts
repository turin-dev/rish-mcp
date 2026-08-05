pluginManagement {
    repositories {
        google()
        mavenCentral()
        gradlePluginPortal()
    }
}
dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        google()
        mavenCentral()
        // libadb-android and sun-security-android (MuntashirAkon) are only
        // published to JitPack, not Maven Central.
        maven { url = uri("https://jitpack.io") }
    }
}
rootProject.name = "rish-mcp-agent"
include(":app")
