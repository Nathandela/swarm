// The Gradle root for the Android client.
//
// It lives under android/ rather than at the repository root so that gradlew, the wrapper
// directory, .gradle/ and Gradle's build outputs do not land in the top level of a Go
// repository -- where `go list ./...` would also have to be taught to ignore them.

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
    }
}

rootProject.name = "swarm-android"

include(":app")
