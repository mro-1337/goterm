# GoTerm for Android

The Android app is a thin Kotlin shell around the same Go program as the desktop GoTerm:

* `libgoterm_engine.so` (in `app/src/main/jniLibs/<abi>/`) is the Go engine, built from the parent folder with
  `-tags engine` (pure Go, no NDK). The app starts it as a child process and talks to it over stdin/stdout
  (protocol in `../engine.go`). The engine does everything: telnet / SSH / RLogin, terminal emulation, ZMODEM,
  XMODEM/YMODEM, the dialing directory, tabs, BBS mouse and audio. It renders frames.
* The Kotlin app (`app/src/main/java/com/goterm/app`) shows the frames, turns touch and the soft keyboard into
  input, plays audio, and handles the clipboard, file import/export and a foreground service that keeps
  connections alive while the app is in the background.

## Build the APK

You need Android Studio (or JDK 17 + Android SDK 34 + Gradle 8.7).

* **Android Studio:** File > Open > this `android` folder, let it sync, then Build > Build APK(s).
  Output: `app/build/outputs/apk/debug/app-debug.apk`.
* **Command line:** `gradle assembleDebug` (or `gradle wrapper --gradle-version 8.7` once, then `./gradlew assembleDebug`).
* **No local tools:** put this project in a GitHub repository, copy `github-workflow-android.yml` to
  `.github/workflows/android.yml`, push, and download the APK from the Actions run ("goterm-apk" artifact).

Install: copy `app-debug.apk` to the phone and open it (allow "install unknown apps"), or `adb install app-debug.apk`.

The engine binaries are already built for arm64-v8a, armeabi-v7a, x86_64 and x86. To rebuild them after changing
the Go code: `sh android/build-engine.sh` from the project root (needs Go only).

## Using it

* Tap = click (menus, tabs, buttons); double tap = double click; drag up/down (or two fingers) = scroll / scrollback;
  long press then drag = select text (copied automatically).
* The bar below the screen has Esc, Tab, sticky Ctrl and Alt, arrows, PgUp/PgDn, Home/End, Ins/Del, F1-F12, the
  keyboard toggle and **Menu** (tabs, quick connect, online menu, paste, upload/download, hang up, file import/export).
* Back = Esc. Quit from the dialing directory with Q (or the menu). `telnet://`, `ssh://` and `rlogin://` links open in GoTerm.
* Downloads land in the app's Download folder; use Menu > "Save a downloaded file..." to copy one to a place of your choice.
  To upload, use Menu > "Add a file to upload..." first, then pick it in the ZMODEM upload dialog.
