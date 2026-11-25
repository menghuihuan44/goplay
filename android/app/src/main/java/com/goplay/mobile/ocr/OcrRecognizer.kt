package com.goplay.mobile.ocr

import android.graphics.Bitmap
import android.graphics.RectF
import com.goplay.mobile.overlay.OverlayController
import com.goplay.mobile.protocol.TextRegion
import com.google.mlkit.vision.common.InputImage
import com.google.mlkit.vision.text.TextRecognition
import com.google.mlkit.vision.text.chinese.ChineseTextRecognizerOptions
import kotlin.math.max

/**
 * On-device OCR for the captured game frame.
 *
 * This is what lets the agent read "开始游戏", "继续游戏", "商城", item names and
 * menu labels instead of blindly tapping a 3x3 grid. Everything stays on the
 * phone: the model is bundled with the APK, no frame is sent to a server.
 *
 * Recognition is best-effort and asynchronous. A busy flag drops frames while
 * the previous recognition is still running, so it never blocks capture.
 */
class OcrRecognizer(private val onResult: (List<TextRegion>) -> Unit) {
    private val recognizer =
        TextRecognition.getClient(ChineseTextRecognizerOptions.Builder().build())

    @Volatile
    private var busy = false

    fun recognize(bitmap: Bitmap) {
        if (busy) {
            bitmap.recycle()
            return
        }
        busy = true
        val width = max(1, bitmap.width)
        val height = max(1, bitmap.height)
        val image = InputImage.fromBitmap(bitmap, 0)
        recognizer.process(image)
            .addOnSuccessListener { result ->
                val out = ArrayList<TextRegion>()
                for (block in result.textBlocks) {
                    for (line in block.lines) {
                        val text = line.text.trim()
                        if (text.isEmpty()) continue
                        val box = line.boundingBox ?: continue
                        if (box.width() < 8 || box.height() < 6) continue
                        val cx = (box.left + box.right) / 2.0 / width
                        val cy = (box.top + box.bottom) / 2.0 / height
                        // Never let the agent tap its own floating pill: the
                        // projection captures the overlay too. Geometry is the
                        // primary guard; text matching is only a fallback for
                        // OCR variants such as "GoPIay" / "Al决策".
                        val w = box.width().toDouble() / width
                        val h = box.height().toDouble() / height
                        if (isOwnOverlay(text, cx, cy, w, h)) continue
                        if (cx < 0.02 || cx > 0.98 || cy < 0.02 || cy > 0.98) continue
                        out.add(TextRegion(text.take(48), cx, cy, w, h))
                        if (out.size >= 24) break
                    }
                    if (out.size >= 24) break
                }
                onResult(out)
            }
            .addOnCompleteListener {
                busy = false
                bitmap.recycle()
            }
    }

    /**
     * True when a recognized line belongs to GoPlay's own overlay. The
     * projection includes the overlay window, so tapping its labels would
     * make the agent operate itself instead of the game. Geometry comes from
     * OverlayController's latest on-screen rectangle; text checks catch OCR
     * spellings that straddle the rectangle edge.
     */
    private fun isOwnOverlay(
        text: String,
        cx: Double,
        cy: Double,
        w: Double,
        h: Double,
    ): Boolean {
        val bounds = OverlayController.screenRect
        if (bounds != null) {
            val left = (cx - w / 2).coerceIn(0.0, 1.0).toFloat()
            val top = (cy - h / 2).coerceIn(0.0, 1.0).toFloat()
            val right = (cx + w / 2).coerceIn(0.0, 1.0).toFloat()
            val bottom = (cy + h / 2).coerceIn(0.0, 1.0).toFloat()
            val box = RectF(left, top, right, bottom)
            if (box.centerX() >= bounds.left && box.centerX() <= bounds.right &&
                box.centerY() >= bounds.top && box.centerY() <= bounds.bottom
            ) return true
            val overlap = RectF(box)
            if (overlap.intersect(bounds)) {
                val area = overlap.width() * overlap.height()
                val own = (right - left) * (bottom - top)
                if (own > 0f && area / own >= 0.30f) return true
            }
        }
        val lower = text.lowercase()
        if (lower.contains("goplay") || lower.contains("gopiay") ||
            lower.contains("正在玩") || lower.contains("ai决策") ||
            lower.contains("ai 决策") || lower.contains("al决策") ||
            lower.contains("al 决策") || lower.contains("al:") ||
            lower.contains("al：")
        ) return true
        if (lower.contains("taptext") || lower.contains("ucb +") ||
            lower.contains("soft preference") || lower.contains("preference weights") ||
            lower.contains("亮度") && lower.contains("边缘")
        ) return true
        if (lower.contains("我来操作") || lower.contains("输入指令") ||
            lower.contains("截屏已停止")
        ) return true
        if (lower.contains("动作") && lower.contains("决策")) return true
        return false
    }

    fun close() {
        try {
            recognizer.close()
        } catch (_: Exception) {
        }
    }
}