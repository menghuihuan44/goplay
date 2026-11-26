package com.goplay.mobile.capture

import android.graphics.Bitmap
import android.media.Image
import java.io.ByteArrayOutputStream

/**
 * Converts one RGBA_8888 [Image] from an ImageReader into a JPEG byte array.
 *
 * MediaProjection's virtual display renders into the ImageReader surface at the
 * already-scaled capture size, so this class does not scale again. Row padding
 * is stripped before compression.
 */
object FrameEncoder {
    /** Copy the RGBA ImageReader plane into a tight ARGB bitmap. The caller
     *  owns the returned bitmap and must recycle it. */
    fun toBitmap(image: Image): Bitmap? {
        if (image.width <= 0 || image.height <= 0) return null
        val plane = image.planes.firstOrNull() ?: return null
        val pixelStride = plane.pixelStride
        if (pixelStride != 4) return null
        val rowStride = plane.rowStride
        val rowPadding = rowStride - pixelStride * image.width
        if (rowPadding < 0) return null
        // copyPixelsFromBuffer() is a native memcpy: a short or already-recycled
        // plane buffer crashes the whole process with SIGSEGV instead of throwing
        // a Java exception. Validate the layout before touching it.
        if (rowPadding % pixelStride != 0) return null
        val paddedWidth = image.width + rowPadding / pixelStride
        if (paddedWidth <= 0) return null
        val rowBytes = paddedWidth.toLong() * 4L
        val required = rowBytes * image.height.toLong()
        if (required <= 0L || required > Int.MAX_VALUE.toLong()) return null

        val buffer = plane.buffer
        if (buffer.remaining().toLong() < required) return null
        buffer.rewind()
        if (buffer.remaining().toLong() < required) return null
        var padded: Bitmap? = null
        try {
            padded = Bitmap.createBitmap(paddedWidth, image.height, Bitmap.Config.ARGB_8888)
            if (padded.rowBytes.toLong() * padded.height.toLong() > buffer.remaining().toLong()) {
                return null
            }
            padded.copyPixelsFromBuffer(buffer)
            if (paddedWidth == image.width) {
                val out = padded
                padded = null
                return out
            }
            return Bitmap.createBitmap(padded, 0, 0, image.width, image.height)
        } catch (e: Throwable) {
            return null
        } finally {
            padded?.recycle()
        }
    }

    fun encode(image: Image, quality: Int): ByteArray? {
        val bitmap = toBitmap(image) ?: return null
        return try {
            val out = ByteArrayOutputStream(bitmap.width * bitmap.height / 4)
            val ok = bitmap.compress(Bitmap.CompressFormat.JPEG, quality.coerceIn(1, 100), out)
            if (!ok) null else out.toByteArray()
        } catch (e: Throwable) {
            null
        } finally {
            bitmap.recycle()
        }
    }
}