package com.goterm.app

import android.media.AudioAttributes
import android.media.AudioFormat
import android.media.AudioTrack
import java.nio.ByteBuffer
import java.nio.ByteOrder
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit

/** Plays the engine's BBS audio stream (PCM) and one-shot WAV sounds. */
class AudioOut {
    private val pcmQueue = LinkedBlockingQueue<ByteArray>()
    private val wavQueue = LinkedBlockingQueue<ByteArray>(16)

    @Volatile
    private var alive = true

    private val pcmThread = Thread({ pcmLoop() }, "audio-pcm")
    private val wavThread = Thread({ wavLoop() }, "audio-wav")

    init {
        pcmThread.isDaemon = true
        wavThread.isDaemon = true
        pcmThread.start()
        wavThread.start()
    }

    fun pcm(data: ByteArray) {
        if (pcmQueue.size < 200) pcmQueue.offer(data)
    }

    fun wav(data: ByteArray) {
        wavQueue.offer(data)
    }

    fun stop() {
        alive = false
        pcmThread.interrupt()
        wavThread.interrupt()
    }

    private fun newTrack(rate: Int, channels: Int, bytes: Int): AudioTrack {
        val mask = if (channels == 1) AudioFormat.CHANNEL_OUT_MONO else AudioFormat.CHANNEL_OUT_STEREO
        val min = AudioTrack.getMinBufferSize(rate, mask, AudioFormat.ENCODING_PCM_16BIT)
        val size = maxOf(min, minOf(bytes, rate * 4))
        return AudioTrack.Builder()
            .setAudioAttributes(
                AudioAttributes.Builder()
                    .setUsage(AudioAttributes.USAGE_MEDIA)
                    .setContentType(AudioAttributes.CONTENT_TYPE_MUSIC)
                    .build()
            )
            .setAudioFormat(
                AudioFormat.Builder()
                    .setEncoding(AudioFormat.ENCODING_PCM_16BIT)
                    .setSampleRate(rate)
                    .setChannelMask(mask)
                    .build()
            )
            .setBufferSizeInBytes(size)
            .setTransferMode(AudioTrack.MODE_STREAM)
            .build()
    }

    private fun pcmLoop() {
        var track: AudioTrack? = null
        try {
            while (alive) {
                val d = pcmQueue.poll(2, TimeUnit.SECONDS)
                if (d == null) {
                    // idle: release the device so other apps can use it
                    track?.let { it.stop(); it.release() }
                    track = null
                    continue
                }
                if (track == null) {
                    track = newTrack(44100, 2, 44100 * 4 / 4)
                    track.play()
                }
                track.write(d, 0, d.size)
            }
        } catch (_: InterruptedException) {
        } catch (_: Exception) {
        } finally {
            try {
                track?.release()
            } catch (_: Exception) {
            }
        }
    }

    private fun wavLoop() {
        try {
            while (alive) {
                val w = wavQueue.take()
                playWav(w)
            }
        } catch (_: InterruptedException) {
        }
    }

    private fun playWav(w: ByteArray) {
        // Minimal RIFF parser: PCM 16-bit only (what GoTerm generates).
        if (w.size < 44 || String(w, 0, 4) != "RIFF") return
        val bb = ByteBuffer.wrap(w).order(ByteOrder.LITTLE_ENDIAN)
        var p = 12
        var rate = 22050
        var ch = 1
        var bits = 16
        var dataOff = -1
        var dataLen = 0
        while (p + 8 <= w.size) {
            val id = String(w, p, 4)
            val sz = bb.getInt(p + 4)
            if (id == "fmt ") {
                ch = bb.getShort(p + 10).toInt()
                rate = bb.getInt(p + 12)
                bits = bb.getShort(p + 22).toInt()
            } else if (id == "data") {
                dataOff = p + 8
                dataLen = minOf(if (sz < 0) w.size else sz, w.size - dataOff)
                break
            }
            if (sz < 0) break
            p += 8 + sz + (sz and 1)
        }
        if (dataOff < 0 || bits != 16 || ch < 1 || ch > 2) return
        try {
            val t = newTrack(rate, ch, dataLen)
            t.play()
            t.write(w, dataOff, dataLen)
            // let the buffered audio finish before releasing
            val ms = dataLen * 1000L / (rate * ch * 2) + 200
            Thread.sleep(ms)
            t.stop()
            t.release()
        } catch (_: Exception) {
        }
    }
}
