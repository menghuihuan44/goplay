package com.goplay.mobile.input

/**
 * Process-wide input gate shared by the floating island, the accessibility
 * service and the activity.
 *
 * The AI must never inject gestures while the user is using GoPlay's own UI
 * (the expanded island, the WebView console) or a keyboard, and it must not
 * touch a window that is not the game the user started.
 *
 * The overlay is exposed through [Overlay] instead of referencing
 * OverlayController directly so the input package stays free of the overlay /
 * accessibility packages (no cyclic dependency).
 */
object InputGate {

    /** The subset of the floating island the accessibility service needs. */
    interface Overlay {
        /** True while the island is expanded (input field / keyboard shown). */
        val isExpanded: Boolean

        /**
         * Marks an AI gesture window: while active the island window becomes
         * FLAG_NOT_TOUCHABLE so the gesture cannot hit it; on false the original
         * touchability is restored.
         */
        fun setAiGesture(active: Boolean)
    }

    /** This app's package name; the AI never pokes its own UI. */
    const val SELF_PACKAGE: String = "com.goplay.mobile"

    /** Attached island, or null while the overlay is not shown. */
    @Volatile
    var currentOverlay: Overlay? = null
        private set

    /**
     * Package of the game the user started from the console. Empty when unknown;
     * in that case only GoPlay itself and keyboard windows are rejected.
     */
    @Volatile
    var activeGamePackage: String = ""
        private set

    /**
     * Package that currently owns the screen, published by the accessibility
     * service. The capture service puts it into every frame so the Go core can
     * stop acting when the user leaves the game.
     */
    @Volatile
    var foregroundPackage: String = ""
        private set

    /** True while the island is expanded and the user may be typing. */
    val isOverlayExpanded: Boolean
        get() = currentOverlay?.isExpanded == true

    fun attach(overlay: Overlay) {
        currentOverlay = overlay
    }

    fun detach(overlay: Overlay) {
        if (currentOverlay === overlay) currentOverlay = null
    }

    fun setActiveGamePackage(pkg: String?) {
        activeGamePackage = pkg?.trim().orEmpty()
    }

    fun setForegroundPackage(pkg: String?) {
        foregroundPackage = pkg?.trim().orEmpty()
    }
}
