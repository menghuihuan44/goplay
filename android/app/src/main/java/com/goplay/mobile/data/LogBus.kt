package com.goplay.mobile.data

import android.util.Log
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update

/** Small in-memory log used by the UI and bug reports. Also mirrors to logcat. */
class LogBus(private val maxLines: Int = 200) {
    private val _lines = MutableStateFlow<List<String>>(emptyList())
    val lines: StateFlow<List<String>> = _lines.asStateFlow()

    private val formatter = SimpleDateFormat("HH:mm:ss", Locale.US)

    fun log(message: String) {
        Log.i(TAG, message)
        val line = synchronized(formatter) { formatter.format(Date()) } + "  " + message
        _lines.update { current -> (current + line).takeLast(maxLines) }
    }

    fun error(message: String, throwable: Throwable? = null) {
        if (throwable != null) Log.e(TAG, message, throwable) else Log.e(TAG, message)
        log("ERROR " + message + (throwable?.let { " (" + it.javaClass.simpleName + ": " + it.message + ")" } ?: ""))
    }

    fun dump(): String = lines.value.joinToString("\n")

    companion object {
        private const val TAG = "GoPlay"
    }
}
