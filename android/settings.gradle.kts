import org.gradle.api.initialization.resolve.RepositoriesMode

// Some networks throttle dl.google.com / repo.maven.apache.org heavily. On a
// developer machine we try the Aliyun mirrors first; on CI (CI=true) the
// official repositories stay first so a stale mirror can never break a release.

pluginManagement {
    val useMirrors = System.getenv("CI").isNullOrBlank()
    repositories {
        if (useMirrors) {
            maven("https://maven.aliyun.com/repository/google")
            maven("https://maven.aliyun.com/repository/public")
        }
        maven("https://maven.aliyun.com/repository/gradle-plugin")
        google()
        mavenCentral()
        gradlePluginPortal()
        if (!useMirrors) {
            maven("https://maven.aliyun.com/repository/google")
            maven("https://maven.aliyun.com/repository/public")
        }
    }
}

dependencyResolutionManagement {
    val useMirrors = System.getenv("CI").isNullOrBlank()
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        if (useMirrors) {
            maven("https://maven.aliyun.com/repository/google")
            maven("https://maven.aliyun.com/repository/public")
        }
        google()
        mavenCentral()
        if (!useMirrors) {
            maven("https://maven.aliyun.com/repository/google")
            maven("https://maven.aliyun.com/repository/public")
        }
    }
}

rootProject.name = "GoPlayMobile"
include(":app")
