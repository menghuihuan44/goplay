# Keep kotlinx.serialization generated serializers for wire models.
-keepattributes *Annotation*, InnerClasses
-dontnote kotlinx.serialization.**
-keepclassmembers class kotlinx.serialization.json.** {
    *** Companion;
}
-keepclasseswithmembers class kotlinx.serialization.json.** {
    kotlinx.serialization.KSerializer serializer(...);
}
-keep,includedescriptorclasses class com.goplay.mobile.**$$serializer { *; }
-keepclassmembers class com.goplay.mobile.** {
    *** Companion;
}
-keepclasseswithmembers class com.goplay.mobile.** {
    kotlinx.serialization.KSerializer serializer(...);
}
