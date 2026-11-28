import { abilityAccessCtrl, common } from '@kit.AbilityKit';
import { webview } from '@kit.ArkWeb';
const appOrigin = 'https://yueying.localhost';
const restoreUrl = appOrigin + '/restore';
@Entry
@Component
struct Room {
  controller: webview.WebviewController = new webview.WebviewController();
  private savedState: string = '';
  private bootstrap: boolean = true;

  private async openApplication(url: string): Promise<void> {
    if (!this.bootstrap) return;
    if (url.startsWith('resource:') && url.endsWith('/restore.html')) {
      try {
        // Transfer only this app's service, account, room and resumable-upload entries.
        this.savedState = await this.controller.runJavaScript(
          "encodeURIComponent(JSON.stringify(Object.entries(localStorage).filter(([key]) => key === 'api-origin' || /^(yueying-account:|yueying-room:|upload:)/.test(key))))"
        );
      } catch {
        this.savedState = '';
      }
      if (this.savedState && this.savedState !== '%5B%5D') {
        this.controller.loadUrl(restoreUrl);
      } else {
        this.bootstrap = false;
        this.controller.loadUrl(appOrigin + '/');
      }
    } else if (url === restoreUrl) {
      try {
        await this.controller.runJavaScript(
          "if (!localStorage.getItem('yueying-origin-migrated')) {" +
          "const entries = JSON.parse(decodeURIComponent(" + JSON.stringify(this.savedState) + "));" +
          "for (const [k,v] of entries) { if (localStorage.getItem(k) === null) localStorage.setItem(k,v); }" +
          "localStorage.setItem('yueying-origin-migrated','1'); }"
        );
      } finally {
        this.savedState = '';
        this.bootstrap = false;
        this.controller.loadUrl(appOrigin + '/');
      }
    }
  }

  build() {
    Column() {
      Web({ src: $rawfile('restore.html'), controller: this.controller })
        .javaScriptAccess(true)
        .domStorageAccess(true)
        .onPageEnd((event) => { if (event) this.openApplication(event.url); })
        .onInterceptRequest((event) => {
          const url = event?.request.getRequestUrl().split(/[?#]/)[0];
          if (url !== appOrigin + '/' && url !== restoreUrl) return null;
          const result = new WebResourceResponse();
          result.setResponseData(url === restoreUrl ? $rawfile('restore.html') : $rawfile('web/index.html'));
          result.setResponseMimeType('text/html');
          result.setResponseEncoding('utf-8');
          result.setResponseCode(200);
          result.setReasonMessage('OK');
          return result;
        })
        .onPermissionRequest(async (event) => {
          const request = event.request;
          const origin = request.getOrigin();
          const audio = ProtectedResourceType.AUDIO_CAPTURE;
          if ((origin !== appOrigin && origin !== appOrigin + '/') ||
              !request.getAccessibleResource().includes(audio)) {
            request.deny();
            return;
          }
          try {
            const context = getContext(this) as common.UIAbilityContext;
            const result = await abilityAccessCtrl.createAtManager()
              .requestPermissionsFromUser(context, ['ohos.permission.MICROPHONE']);
            if (result.authResults[0] === 0) {
              request.grant([audio]);
              return;
            }
          } catch {
            // An unavailable permission service leaves capture disabled.
          }
          request.deny();
        })
        .width('100%')
        .height('100%')
    }
  }
}
