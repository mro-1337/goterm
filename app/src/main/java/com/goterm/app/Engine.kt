package com.goterm.app

import android.content.Context
import android.net.ConnectivityManager
import android.util.Log
import java.io.BufferedInputStream
import java.io.DataInputStream
import java.io.File
import java.io.OutputStream
import java.nio.ByteBuffer
import java.nio.ByteOrder
import java.util.TimeZone
import java.util.concurrent.Executors

/**
 * Runs the Go engine (libgoterm_engine.so) as a child process and speaks its
 * length-prefixed protocol over stdin/stdout. See engine.go for the format.
 */
class Engine(private val ctx: Context) {

    interface Listener {
        fun onFrame(w: Int, h: Int, px: IntArray)
        fun onTitle(title: String)
        fun onClipSet(text: String)
        fun onClipReq(): String
        fun onWav(data: ByteArray)
        fun onPcm(data: ByteArray)
        fun onQuit()
    }

    @Volatile
    var listener: Listener? = null

    @Volatile
    var running = false
        private set

    // Last state, replayed when the activity is recreated.
    @Volatile
    var lastW = 0
    @Volatile
    var lastH = 0
    @Volatile
    var lastPx: IntArray? = null
    @Volatile
    var lastTitle = "GoTerm"

    val downloadDir: File = File(ctx.getExternalFilesDir(null) ?: ctx.filesDir, "Download")
    val uploadDir: File = File(ctx.filesDir, "Uploads")

    private var proc: Process? = null
    private var out: OutputStream? = null
    private val writer = Executors.newSingleThreadExecutor()

    @Synchronized
    fun start(): Boolean {
        if (running) return true
        val bin = File(ctx.applicationInfo.nativeLibraryDir, "libgoterm_engine.so")
        if (!bin.exists()) {
            Log.e(TAG, "engine binary missing: $bin")
            return false
        }
        downloadDir.mkdirs()
        uploadDir.mkdirs()
        val pb = ProcessBuilder(bin.absolutePath)
        val env = pb.environment()
        env["HOME"] = ctx.filesDir.absolutePath
        env["XDG_CONFIG_HOME"] = File(ctx.filesDir, "config").absolutePath
        env["XDG_CACHE_HOME"] = ctx.cacheDir.absolutePath
        env["TMPDIR"] = ctx.cacheDir.absolutePath
        env["TZ"] = TimeZone.getDefault().id
        env["GOTERM_DOWNDIR"] = downloadDir.absolutePath
        env["GOTERM_UPLOADDIR"] = uploadDir.absolutePath
        env["GOTERM_DNS"] = dnsServers()
        val p = try {
            pb.start()
        } catch (e: Exception) {
            Log.e(TAG, "cannot start engine", e)
            return false
        }
        proc = p
        out = p.outputStream
        running = true
        Thread({ drainErr(p) }, "engine-stderr").start()
        Thread({ readLoop(p) }, "engine-reader").start()
        return true
    }

    private fun dnsServers(): String {
        val list = ArrayList<String>()
        try {
            val cm = ctx.getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
            val net = cm.activeNetwork
            if (net != null) {
                cm.getLinkProperties(net)?.dnsServers?.forEach { a ->
                    a.hostAddress?.let { list.add(it) }
                }
            }
        } catch (e: Exception) {
            Log.w(TAG, "dns lookup failed", e)
        }
        list.add("8.8.8.8")
        list.add("1.1.1.1")
        return list.joinToString(",")
    }

    private fun drainErr(p: Process) {
        try {
            p.errorStream.bufferedReader().forEachLine { Log.w(TAG, "engine: $it") }
        } catch (_: Exception) {
        }
    }

    private fun readLoop(p: Process) {
        val din = DataInputStream(BufferedInputStream(p.inputStream, 1 shl 16))
        try {
            while (true) {
                val type = din.readUnsignedByte()
                val len = din.readInt()
                if (len < 0 || len > (64 shl 20)) break
                val payload = ByteArray(len)
                din.readFully(payload)
                dispatch(type, payload)
            }
        } catch (_: Exception) {
        }
        running = false
        listener?.onQuit()
    }

    private fun dispatch(type: Int, p: ByteArray) {
        when (type) {
            1 -> { // FRAME
                val bb = ByteBuffer.wrap(p)
                val w = bb.int
                val h = bb.int
                if (w <= 0 || h <= 0 || p.size < 8 + w * h * 4) return
                val px = IntArray(w * h)
                ByteBuffer.wrap(p, 8, w * h * 4).order(ByteOrder.LITTLE_ENDIAN).asIntBuffer().get(px)
                lastW = w
                lastH = h
                lastPx = px
                listener?.onFrame(w, h, px)
            }
            2 -> {
                lastTitle = String(p, Charsets.UTF_8)
                listener?.onTitle(lastTitle)
            }
            3 -> listener?.onClipSet(String(p, Charsets.UTF_8))
            4 -> {
                val t = listener?.onClipReq() ?: ""
                send(3, t.toByteArray(Charsets.UTF_8))
            }
            5 -> listener?.onWav(p)
            6 -> listener?.onPcm(p)
            7 -> listener?.onQuit()
        }
    }

    private fun send(type: Int, payload: ByteArray) {
        if (!running) return
        writer.execute {
            try {
                val o = out ?: return@execute
                val hdr = ByteBuffer.allocate(5 + payload.size)
                hdr.put(type.toByte()).putInt(payload.size).put(payload)
                o.write(hdr.array())
                o.flush()
            } catch (_: Exception) {
            }
        }
    }

    /** key: see engineKeys in engine.go (0 = none). flags: 1 ctrl, 2 alt, 4 shift. */
    fun sendKey(key: Int, ch: Int, flags: Int) {
        val b = ByteBuffer.allocate(6)
        b.put(key.toByte()).putInt(ch).put(flags.toByte())
        send(1, b.array())
    }

    /** flags: 1 down, 2 up, 4 move, 8 double, 16 shift, 32 ctrl, 64 alt. */
    fun sendMouse(flags: Int, button: Int, wheel: Int, x: Int, y: Int) {
        val b = ByteBuffer.allocate(11)
        b.put(flags.toByte()).put(button.toByte()).put(wheel.toByte()).putInt(x).putInt(y)
        send(2, b.array())
    }

    fun sendText(t: String) = send(4, t.toByteArray(Charsets.UTF_8))
    fun sendConnect(target: String) = send(7, target.toByteArray(Charsets.UTF_8))
    fun pause() = send(5, ByteArray(0))
    fun resume() = send(6, ByteArray(0))

    @Synchronized
    fun stop() {
        running = false
        try {
            out?.close()
        } catch (_: Exception) {
        }
        proc?.destroy()
        proc = null
    }

    companion object {
        const val TAG = "GoTerm"

        // Key codes shared with engine.go
        const val K_UP = 1
        const val K_DOWN = 2
        const val K_LEFT = 3
        const val K_RIGHT = 4
        const val K_HOME = 5
        const val K_END = 6
        const val K_PGUP = 7
        const val K_PGDN = 8
        const val K_INS = 9
        const val K_DEL = 10
        const val K_ENTER = 11
        const val K_ESC = 12
        const val K_BS = 13
        const val K_TAB = 14
        const val K_F1 = 15 // F1..F12 = 15..26

        const val F_CTRL = 1
        const val F_ALT = 2
        const val F_SHIFT = 4

        const val M_DOWN = 1
        const val M_UP = 2
        const val M_MOVE = 4
        const val M_DOUBLE = 8
        const val M_SHIFT = 16
    }
}
