package com.goterm.app

import android.content.Context
import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.Paint
import android.graphics.Rect
import android.os.Handler
import android.os.Looper
import android.text.InputType
import android.view.HapticFeedbackConstants
import android.view.KeyEvent
import android.view.MotionEvent
import android.view.View
import android.view.ViewConfiguration
import android.view.inputmethod.BaseInputConnection
import android.view.inputmethod.EditorInfo
import android.view.inputmethod.InputConnection
import android.view.inputmethod.InputMethodManager

/**
 * Shows the engine's framebuffer, scaled to fit, and turns touch and keyboard
 * input into engine messages.
 *
 * Touch: tap = click, double tap = double click, drag up/down = scroll wheel,
 * long press then drag = select text (copies on release).
 */
class TerminalView(context: Context, private val engine: Engine) : View(context) {

    private var bitmap: Bitmap? = null
    private var fbW = 0
    private var fbH = 0
    private val dest = Rect()
    private val paint = Paint().apply { isFilterBitmap = true }

    // Sticky modifiers set by the key bar.
    var ctrlLatch = false
    var altLatch = false
    var onLatchChanged: (() -> Unit)? = null

    private val handler = Handler(Looper.getMainLooper())
    private val slop = ViewConfiguration.get(context).scaledTouchSlop
    private val longPress = ViewConfiguration.getLongPressTimeout().toLong()
    private val wheelStep = 28f * resources.displayMetrics.density

    private var downX = 0f
    private var downY = 0f
    private var lastY = 0f
    private var scrollAcc = 0f
    private var dragging = false
    private var selecting = false
    private var longFired = false
    private var lastTapTime = 0L
    private var lastTapX = 0f
    private var lastTapY = 0f
    private var doubleTap = false

    init {
        isFocusable = true
        isFocusableInTouchMode = true
        setBackgroundColor(Color.BLACK)
    }

    fun setFrame(w: Int, h: Int, px: IntArray) {
        var b = bitmap
        if (b == null || fbW != w || fbH != h) {
            b = Bitmap.createBitmap(w, h, Bitmap.Config.ARGB_8888)
            bitmap = b
            fbW = w
            fbH = h
            layoutDest()
        }
        b!!.setPixels(px, 0, w, 0, 0, w, h)
        invalidate()
    }

    override fun onSizeChanged(w: Int, h: Int, oldw: Int, oldh: Int) {
        super.onSizeChanged(w, h, oldw, oldh)
        layoutDest()
    }

    private fun layoutDest() {
        if (fbW == 0 || fbH == 0 || width == 0 || height == 0) return
        val scale = minOf(width.toFloat() / fbW, height.toFloat() / fbH)
        val dw = (fbW * scale).toInt()
        val dh = (fbH * scale).toInt()
        val l = (width - dw) / 2
        val t = 0 // keep the screen at the top so the keyboard does not cover it
        dest.set(l, t, l + dw, t + dh)
    }

    override fun onDraw(canvas: Canvas) {
        val b = bitmap ?: return
        canvas.drawBitmap(b, null, dest, paint)
    }

    // ------------------------------------------------------------ touch

    private fun fbX(x: Float): Int =
        if (dest.width() > 0) ((x - dest.left) * fbW / dest.width()).toInt() else 0

    private fun fbY(y: Float): Int =
        if (dest.height() > 0) ((y - dest.top) * fbH / dest.height()).toInt() else 0

    private val longPressRunnable = Runnable {
        if (!dragging) {
            longFired = true
            selecting = true
            performHapticFeedback(HapticFeedbackConstants.LONG_PRESS)
            engine.sendMouse(Engine.M_DOWN or Engine.M_SHIFT, 0, 0, fbX(downX), fbY(downY))
        }
    }

    override fun onTouchEvent(e: MotionEvent): Boolean {
        if (e.pointerCount > 1) {
            // two fingers: scroll
            handler.removeCallbacks(longPressRunnable)
            if (e.actionMasked == MotionEvent.ACTION_MOVE) {
                val y = (e.getY(0) + e.getY(1)) / 2
                scrollBy(y - lastY)
                lastY = y
            } else {
                lastY = (e.getY(0) + e.getY(1)) / 2
            }
            return true
        }
        when (e.actionMasked) {
            MotionEvent.ACTION_DOWN -> {
                requestFocus()
                downX = e.x
                downY = e.y
                lastY = e.y
                scrollAcc = 0f
                dragging = false
                selecting = false
                longFired = false
                val now = e.eventTime
                doubleTap = now - lastTapTime < 350 &&
                        Math.abs(e.x - lastTapX) < slop * 3 && Math.abs(e.y - lastTapY) < slop * 3
                handler.postDelayed(longPressRunnable, longPress)
            }
            MotionEvent.ACTION_MOVE -> {
                if (selecting) {
                    engine.sendMouse(Engine.M_MOVE or Engine.M_SHIFT, 0, 0, fbX(e.x), fbY(e.y))
                } else {
                    if (!dragging && (Math.abs(e.x - downX) > slop || Math.abs(e.y - downY) > slop)) {
                        dragging = true
                        handler.removeCallbacks(longPressRunnable)
                    }
                    if (dragging) {
                        scrollBy(e.y - lastY)
                        lastY = e.y
                    }
                }
            }
            MotionEvent.ACTION_UP -> {
                handler.removeCallbacks(longPressRunnable)
                if (selecting) {
                    engine.sendMouse(Engine.M_UP or Engine.M_SHIFT, 0, 0, fbX(e.x), fbY(e.y))
                } else if (!dragging) {
                    val x = fbX(e.x)
                    val y = fbY(e.y)
                    val dbl = if (doubleTap) Engine.M_DOUBLE else 0
                    engine.sendMouse(Engine.M_DOWN or dbl, 0, 0, x, y)
                    engine.sendMouse(Engine.M_UP, 0, 0, x, y)
                    if (doubleTap) {
                        lastTapTime = 0
                    } else {
                        lastTapTime = e.eventTime
                        lastTapX = e.x
                        lastTapY = e.y
                    }
                }
                selecting = false
                dragging = false
            }
            MotionEvent.ACTION_CANCEL -> {
                handler.removeCallbacks(longPressRunnable)
                if (selecting) engine.sendMouse(Engine.M_UP or Engine.M_SHIFT, 0, 0, fbX(e.x), fbY(e.y))
                selecting = false
                dragging = false
            }
        }
        return true
    }

    private fun scrollBy(dy: Float) {
        scrollAcc += dy
        while (scrollAcc >= wheelStep) {
            scrollAcc -= wheelStep
            engine.sendMouse(0, 0, 1, fbX(width / 2f), fbY(height / 2f)) // finger down = older text
        }
        while (scrollAcc <= -wheelStep) {
            scrollAcc += wheelStep
            engine.sendMouse(0, 0, -1, fbX(width / 2f), fbY(height / 2f))
        }
    }

    // --------------------------------------------------------- keyboard

    fun showKeyboard() {
        requestFocus()
        val imm = context.getSystemService(Context.INPUT_METHOD_SERVICE) as InputMethodManager
        imm.showSoftInput(this, InputMethodManager.SHOW_FORCED)
    }

    fun toggleKeyboard() {
        requestFocus()
        val imm = context.getSystemService(Context.INPUT_METHOD_SERVICE) as InputMethodManager
        imm.toggleSoftInput(InputMethodManager.SHOW_FORCED, 0)
    }

    override fun onCheckIsTextEditor(): Boolean = true

    override fun onCreateInputConnection(outAttrs: EditorInfo): InputConnection {
        outAttrs.inputType = InputType.TYPE_CLASS_TEXT or
                InputType.TYPE_TEXT_VARIATION_VISIBLE_PASSWORD or
                InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS
        outAttrs.imeOptions = EditorInfo.IME_FLAG_NO_FULLSCREEN or
                EditorInfo.IME_FLAG_NO_EXTRACT_UI or EditorInfo.IME_ACTION_NONE
        return TermInput(this)
    }

    private inner class TermInput(v: View) : BaseInputConnection(v, false) {
        private var composing = ""

        private fun apply(t: String) {
            var i = 0
            while (i < composing.length && i < t.length && composing[i] == t[i]) i++
            repeat(composing.length - i) { key(Engine.K_BS, 0) }
            if (i < t.length) typeText(t.substring(i))
            composing = t
        }

        override fun commitText(text: CharSequence?, newCursorPosition: Int): Boolean {
            apply(text?.toString() ?: "")
            composing = ""
            return true
        }

        override fun setComposingText(text: CharSequence?, newCursorPosition: Int): Boolean {
            apply(text?.toString() ?: "")
            return true
        }

        override fun finishComposingText(): Boolean {
            composing = ""
            return true
        }

        override fun deleteSurroundingText(beforeLength: Int, afterLength: Int): Boolean {
            repeat(maxOf(beforeLength, 1)) { key(Engine.K_BS, 0) }
            composing = ""
            return true
        }

        override fun sendKeyEvent(event: KeyEvent): Boolean {
            if (event.action == KeyEvent.ACTION_DOWN) handleKeyEvent(event)
            return true
        }
    }

    private fun mods(): Int {
        var f = 0
        if (ctrlLatch) f = f or Engine.F_CTRL
        if (altLatch) f = f or Engine.F_ALT
        return f
    }

    private fun clearLatch() {
        if (ctrlLatch || altLatch) {
            ctrlLatch = false
            altLatch = false
            onLatchChanged?.invoke()
        }
    }

    /** Special key with the sticky modifiers applied. */
    fun key(code: Int, extra: Int) {
        engine.sendKey(code, 0, mods() or extra)
        clearLatch()
    }

    fun typeText(s: String) {
        var i = 0
        while (i < s.length) {
            val cp = s.codePointAt(i)
            i += Character.charCount(cp)
            when (cp) {
                '\n'.code, '\r'.code -> key(Engine.K_ENTER, 0)
                '\t'.code -> key(Engine.K_TAB, 0)
                else -> typeChar(cp)
            }
        }
    }

    private fun typeChar(cp: Int) {
        var f = mods()
        var c = cp
        if (f and Engine.F_CTRL != 0) {
            // Ctrl+letter is sent as control code 1..26 (see KeyEvent in ui.go)
            if (cp in 'a'.code..'z'.code) c = cp - 'a'.code + 1
            else if (cp in 'A'.code..'Z'.code) c = cp - 'A'.code + 1
            else if (cp == '['.code) c = 27
            else if (cp == ']'.code) c = 29
        }
        engine.sendKey(0, c, f)
        clearLatch()
    }

    private fun handleKeyEvent(e: KeyEvent): Boolean {
        val code = when (e.keyCode) {
            KeyEvent.KEYCODE_DPAD_UP -> Engine.K_UP
            KeyEvent.KEYCODE_DPAD_DOWN -> Engine.K_DOWN
            KeyEvent.KEYCODE_DPAD_LEFT -> Engine.K_LEFT
            KeyEvent.KEYCODE_DPAD_RIGHT -> Engine.K_RIGHT
            KeyEvent.KEYCODE_MOVE_HOME -> Engine.K_HOME
            KeyEvent.KEYCODE_MOVE_END -> Engine.K_END
            KeyEvent.KEYCODE_PAGE_UP -> Engine.K_PGUP
            KeyEvent.KEYCODE_PAGE_DOWN -> Engine.K_PGDN
            KeyEvent.KEYCODE_INSERT -> Engine.K_INS
            KeyEvent.KEYCODE_FORWARD_DEL -> Engine.K_DEL
            KeyEvent.KEYCODE_ENTER, KeyEvent.KEYCODE_NUMPAD_ENTER -> Engine.K_ENTER
            KeyEvent.KEYCODE_ESCAPE -> Engine.K_ESC
            KeyEvent.KEYCODE_DEL -> Engine.K_BS
            KeyEvent.KEYCODE_TAB -> Engine.K_TAB
            in KeyEvent.KEYCODE_F1..KeyEvent.KEYCODE_F12 -> Engine.K_F1 + (e.keyCode - KeyEvent.KEYCODE_F1)
            else -> 0
        }
        var f = 0
        if (e.isCtrlPressed) f = f or Engine.F_CTRL
        if (e.isAltPressed) f = f or Engine.F_ALT
        if (e.isShiftPressed) f = f or Engine.F_SHIFT
        if (code != 0) {
            engine.sendKey(code, 0, f or mods())
            clearLatch()
            return true
        }
        val uc = e.getUnicodeChar(e.metaState and (KeyEvent.META_CTRL_MASK or KeyEvent.META_ALT_MASK).inv())
        if (uc > 0) {
            if (f and Engine.F_CTRL != 0) {
                val up = Character.toUpperCase(uc)
                val c = if (up in 'A'.code..'Z'.code) up - 'A'.code + 1 else uc
                engine.sendKey(0, c, f)
            } else {
                engine.sendKey(0, uc, f or mods())
            }
            clearLatch()
            return true
        }
        return false
    }

    override fun onKeyDown(keyCode: Int, event: KeyEvent): Boolean {
        if (keyCode == KeyEvent.KEYCODE_BACK) return false // handled by the activity
        if (handleKeyEvent(event)) return true
        return super.onKeyDown(keyCode, event)
    }
}
