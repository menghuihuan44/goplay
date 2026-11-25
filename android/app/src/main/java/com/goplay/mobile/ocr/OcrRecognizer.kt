package com.goplay.mobile.ocr

import android.graphics.Bitmap
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
                        // Never let the agent tap its own floating pill: the
                        // projection captures the overlay too.
                        if (text.contains("GoPlay") || text.contains("AI 决策") ||
                            text.contains("截屏已停止") || text.contains("已连接")
                        ) continue
                        val box = line.boundingBox ?: continue
                        if (box.width() < 8 || box.height() < 6) continue
                        val cx = (box.left + box.right) / 2.0 / width
                        val cy = (box.top + box.bottom) / 2.0 / height
                        val w = box.width().toDouble() / width
                        val h = box.height().toDouble() / height
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

    fun close() {
        try {
            recognizer.close()
        } catch (_: Exception) {
        }
    }
}