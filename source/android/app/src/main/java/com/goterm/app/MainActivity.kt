package com.goterm.app

import android.Manifest
import android.app.Activity
import android.app.AlertDialog
import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.content.Intent
import android.graphics.Color
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.provider.OpenableColumns
import android.view.Gravity
import android.view.PopupMenu
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.HorizontalScrollView
import android.widget.LinearLayout
import android.widget.Toast
import java.io.File
import java.util.concurrent.FutureTask
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicReference

/** Process-wide singletons so the engine survives activity re-creation. */
object Host {
    var engine: Engine? = null
    var audio: AudioOut? = null
}

class MainActivity : Activity(), Engine.Listener {

    private lateinit var engine: Engine
    private lateinit var term: TerminalView
    private lateinit var ctrlBtn: Button
    private lateinit var altBtn: Button
    private val ui = Handler(Looper.getMainLooper())
    private val pendingFrame = AtomicReference<Triple<Int, Int, IntArray>?>(null)
    private var saveSource: File? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val app = applicationContext
        if (Host.engine == null) Host.engine = Engine(app)
        if (Host.audio == null) Host.audio = AudioOut()
        engine = Host.engine!!

        term = TerminalView(this, engine)
        term.onLatchChanged = { updateLatchButtons() }

        val root = LinearLayout(this)
        root.orientation = LinearLayout.VERTICAL
        root.setBackgroundColor(Color.BLACK)
        root.addView(term, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f))
        root.addView(buildKeyBar(), LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        setContentView(root)

        if (Build.VERSION.SDK_INT >= 33) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 1)
        }
        engine.listener = this
        if (!engine.start()) {
            Toast.makeText(this, "Could not start the GoTerm engine", Toast.LENGTH_LONG).show()
        } else {
            startService(Intent(this, KeepAliveService::class.java))
        }
        // Show whatever the engine last drew (after rotation / re-open).
        engine.lastPx?.let { term.setFrame(engine.lastW, engine.lastH, it) }
        title = engine.lastTitle
        handleIntent(intent)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        handleIntent(intent)
    }

    private fun handleIntent(i: Intent?) {
        val u = i?.data ?: return
        if (i.action == Intent.ACTION_VIEW) {
            engine.sendConnect(u.toString())
            i.data = null
        }
    }

    override fun onResume() {
        super.onResume()
        engine.listener = this
        engine.resume()
    }

    override fun onPause() {
        engine.pause()
        super.onPause()
    }

    override fun onDestroy() {
        if (isFinishing) {
            engine.listener = null
        }
        super.onDestroy()
    }

    @Deprecated("Back sends Esc to the terminal")
    override fun onBackPressed() {
        engine.sendKey(Engine.K_ESC, 0, 0)
    }

    // ------------------------------------------------------ engine events

    override fun onFrame(w: Int, h: Int, px: IntArray) {
        val first = pendingFrame.getAndSet(Triple(w, h, px)) == null
        if (first) {
            ui.post {
                val f = pendingFrame.getAndSet(null)
                if (f != null) term.setFrame(f.first, f.second, f.third)
            }
        }
    }

    override fun onTitle(title: String) {
        ui.post { setTitle(title) }
    }

    override fun onClipSet(text: String) {
        ui.post {
            val cm = getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
            cm.setPrimaryClip(ClipData.newPlainText("GoTerm", text))
        }
    }

    override fun onClipReq(): String {
        val task = FutureTask<String> {
            val cm = getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
            val c = cm.primaryClip
            if (c != null && c.itemCount > 0) c.getItemAt(0).coerceToText(this).toString() else ""
        }
        ui.post(task)
        return try {
            task.get(1, TimeUnit.SECONDS)
        } catch (e: Exception) {
            ""
        }
    }

    override fun onWav(data: ByteArray) {
        Host.audio?.wav(data)
    }

    override fun onPcm(data: ByteArray) {
        Host.audio?.pcm(data)
    }

    override fun onQuit() {
        ui.post {
            stopService(Intent(this, KeepAliveService::class.java))
            engine.stop()
            Host.engine = null
            finish()
        }
    }

    // ------------------------------------------------------------ key bar

    private fun bar(label: String, action: (View) -> Unit): Button {
        val b = Button(this)
        b.text = label
        b.isAllCaps = false
        b.textSize = 13f
        b.setTextColor(Color.WHITE)
        b.setBackgroundColor(0xFF2A2A66.toInt())
        b.minWidth = 0
        b.minimumWidth = 0
        b.setPadding(24, 8, 24, 8)
        val lp = LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT)
        lp.setMargins(3, 3, 3, 3)
        b.layoutParams = lp
        b.setOnClickListener { action(it) }
        b.isFocusable = false
        return b
    }

    private fun buildKeyBar(): View {
        val row = LinearLayout(this)
        row.orientation = LinearLayout.HORIZONTAL
        row.gravity = Gravity.CENTER_VERTICAL
        row.setBackgroundColor(0xFF111133.toInt())
        fun k(code: Int) = { _: View -> term.key(code, 0) }
        row.addView(bar("Menu") { showMenu(it) })
        row.addView(bar("Keyboard") { term.toggleKeyboard() })
        row.addView(bar("Esc", k(Engine.K_ESC)))
        row.addView(bar("Tab", k(Engine.K_TAB)))
        ctrlBtn = bar("Ctrl") { term.ctrlLatch = !term.ctrlLatch; updateLatchButtons() }
        altBtn = bar("Alt") { term.altLatch = !term.altLatch; updateLatchButtons() }
        row.addView(ctrlBtn)
        row.addView(altBtn)
        row.addView(bar("←", k(Engine.K_LEFT)))
        row.addView(bar("↑", k(Engine.K_UP)))
        row.addView(bar("↓", k(Engine.K_DOWN)))
        row.addView(bar("→", k(Engine.K_RIGHT)))
        row.addView(bar("PgUp", k(Engine.K_PGUP)))
        row.addView(bar("PgDn", k(Engine.K_PGDN)))
        row.addView(bar("Home", k(Engine.K_HOME)))
        row.addView(bar("End", k(Engine.K_END)))
        row.addView(bar("Ins", k(Engine.K_INS)))
        row.addView(bar("Del", k(Engine.K_DEL)))
        row.addView(bar("F1-F12") { v ->
            val m = PopupMenu(this, v)
            for (i in 1..12) m.menu.add(0, i, i, "F$i")
            m.setOnMenuItemClickListener { term.key(Engine.K_F1 + it.itemId - 1, 0); true }
            m.show()
        })
        val sv = HorizontalScrollView(this)
        sv.isHorizontalScrollBarEnabled = false
        sv.addView(row)
        return sv
    }

    private fun updateLatchButtons() {
        ctrlBtn.setBackgroundColor(if (term.ctrlLatch) 0xFFAA5500.toInt() else 0xFF2A2A66.toInt())
        altBtn.setBackgroundColor(if (term.altLatch) 0xFFAA5500.toInt() else 0xFF2A2A66.toInt())
    }

    private fun alt(ch: Char) = engine.sendKey(0, ch.code, Engine.F_ALT)

    private fun showMenu(anchor: View) {
        val m = PopupMenu(this, anchor)
        val items = listOf(
            "New tab" to { alt('t') },
            "Close tab" to { alt('w') },
            "Previous tab" to { engine.sendKey(Engine.K_LEFT, 0, Engine.F_ALT) },
            "Next tab" to { engine.sendKey(Engine.K_RIGHT, 0, Engine.F_ALT) },
            "Quick connect (Alt+Q)" to { alt('q') },
            "Online menu (Alt+Z)" to { alt('z') },
            "Paste" to { alt('v') },
            "Upload (Alt+U)" to { alt('u') },
            "Download (Alt+D)" to { alt('d') },
            "Hang up (Alt+H)" to { alt('h') },
            "Add a file to upload..." to { pickUploadFiles() },
            "Save a downloaded file..." to { chooseDownload() },
            "Help (F1)" to { engine.sendKey(Engine.K_F1, 0, 0) }
        )
        items.forEachIndexed { i, p -> m.menu.add(0, i, i, p.first) }
        m.setOnMenuItemClickListener { items[it.itemId].second(); true }
        m.show()
    }

    // --------------------------------------------------- files (SAF)

    private fun pickUploadFiles() {
        val i = Intent(Intent.ACTION_OPEN_DOCUMENT)
        i.addCategory(Intent.CATEGORY_OPENABLE)
        i.type = "*/*"
        i.putExtra(Intent.EXTRA_ALLOW_MULTIPLE, true)
        startActivityForResult(i, REQ_UPLOAD)
    }

    private fun chooseDownload() {
        val files = (engine.downloadDir.listFiles() ?: emptyArray()).filter { it.isFile }
            .sortedByDescending { it.lastModified() }
        if (files.isEmpty()) {
            Toast.makeText(this, "No downloaded files yet", Toast.LENGTH_SHORT).show()
            return
        }
        AlertDialog.Builder(this)
            .setTitle("Save which file?")
            .setItems(files.map { it.name }.toTypedArray()) { _, which ->
                saveSource = files[which]
                val i = Intent(Intent.ACTION_CREATE_DOCUMENT)
                i.addCategory(Intent.CATEGORY_OPENABLE)
                i.type = "application/octet-stream"
                i.putExtra(Intent.EXTRA_TITLE, files[which].name)
                startActivityForResult(i, REQ_SAVE)
            }
            .setNegativeButton("Cancel", null)
            .show()
    }

    @Deprecated("Uses the framework result API to avoid AndroidX")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        if (resultCode != RESULT_OK || data == null) return
        when (requestCode) {
            REQ_UPLOAD -> {
                val uris = ArrayList<Uri>()
                data.data?.let { uris.add(it) }
                data.clipData?.let { c -> for (n in 0 until c.itemCount) uris.add(c.getItemAt(n).uri) }
                Thread {
                    var n = 0
                    for (u in uris) {
                        if (copyIn(u)) n++
                    }
                    ui.post {
                        Toast.makeText(
                            this,
                            "Added $n file(s). Choose them from ${engine.uploadDir.absolutePath} in the upload dialog.",
                            Toast.LENGTH_LONG
                        ).show()
                    }
                }.start()
            }
            REQ_SAVE -> {
                val dst = data.data ?: return
                val src = saveSource ?: return
                Thread {
                    val ok = try {
                        contentResolver.openOutputStream(dst)?.use { o -> src.inputStream().use { it.copyTo(o) } }
                        true
                    } catch (e: Exception) {
                        false
                    }
                    ui.post {
                        Toast.makeText(this, if (ok) "Saved ${src.name}" else "Save failed", Toast.LENGTH_SHORT).show()
                    }
                }.start()
            }
        }
    }

    private fun copyIn(u: Uri): Boolean {
        return try {
            var name = "upload.bin"
            contentResolver.query(u, null, null, null, null)?.use { c ->
                val col = c.getColumnIndex(OpenableColumns.DISPLAY_NAME)
                if (col >= 0 && c.moveToFirst()) name = c.getString(col) ?: name
            }
            name = name.replace('/', '_')
            engine.uploadDir.mkdirs()
            contentResolver.openInputStream(u)?.use { i ->
                File(engine.uploadDir, name).outputStream().use { o -> i.copyTo(o) }
            }
            true
        } catch (e: Exception) {
            false
        }
    }

    companion object {
        const val REQ_UPLOAD = 11
        const val REQ_SAVE = 12
    }
}
