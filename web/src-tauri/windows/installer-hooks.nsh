; The GNU toolchain links WebView2Loader dynamically. Cargo places the
; architecture-matched DLL beside the executable. MSVC links it statically.
!macro NSIS_HOOK_POSTINSTALL
  !searchreplace YUEYING_WEBVIEW_LOADER "${MAINBINARYSRCPATH}" "${MAINBINARYNAME}.exe" "WebView2Loader.dll"
  !if /FileExists "${YUEYING_WEBVIEW_LOADER}"
    SetOutPath "$INSTDIR"
    File "/oname=WebView2Loader.dll" "${YUEYING_WEBVIEW_LOADER}"
  !endif
!macroend

!macro NSIS_HOOK_PREUNINSTALL
  Delete "$INSTDIR\WebView2Loader.dll"
!macroend
