import { abilityAccessCtrl, common } from '@kit.AbilityKit';
import { webview } from '@kit.ArkWeb';
import { preferences } from '@kit.ArkData';
const appOrigin = 'https://yueying.localhost';
const restoreUrl = appOrigin + '/restore';
@Entry
@Component
struct Room {
  controller: webview.WebviewController = new webview.WebviewController();
  private savedState: string = '';
  private bootstrap: boolean = true;
  private settings?: preferences.Preferences;

  aboutToAppear(): void {
    try {
      this.settings = preferences.getPreferencesSync(getContext(this), { name: 'web-origin' });
      this.bootstrap = this.settings.getSync('migrated', false) !== true;
    } catch {
      this.bootstrap = true;
    }
  }

  private async openApplication(url: string): Promise<void> {
    if (!this.bootstrap) return;
    if (url.startsWith('resource:') && url.endsWith('/web/index.html')) {
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
        const migrated = await this.controller.runJavaScript(
          "if (!localStorage.getItem('yueying-origin-migrated')) {" +
          "const raw = " + JSON.stringify(this.savedState) + ";" +
          "const encoded = raw.startsWith('\"') ? JSON.parse(raw) : raw;" +
          "const entries = JSON.parse(decodeURIComponent(encoded));" +
          "for (const [k,v] of entries) { if (localStorage.getItem(k) === null) localStorage.setItem(k,v); }" +
          "localStorage.setItem('yueying-origin-migrated','1'); } " +
          "localStorage.getItem('yueying-origin-migrated') === '1';"
        );
        if (migrated === 'true') {
          this.settings?.putSync('migrated', true);
          this.settings?.flushSync();
        }
      } catch {
        // Keep the legacy storage intact for another attempt on next launch.
      } finally {
        this.savedState = '';
        this.bootstrap = false;
        this.controller.loadUrl(appOrigin + '/');
      }
    }
  }

  build() {
    Column() {
      Web({ src: this.bootstrap ? $rawfile('web/index.html') : appOrigin + '/', controller: this.controller })
        .javaScriptAccess(true)
        .domStorageAccess(true)
        .onPageEnd((event) => { if (event) this.openApplication(event.url); })
        .onInterceptRequest((event) => {
          const url = event?.request.getRequestUrl().split(/[?#]/)[0];
          const legacy = this.bootstrap && url?.startsWith('resource:') && url.endsWith('/web/index.html');
          if (!legacy && url !== appOrigin + '/' && url !== restoreUrl) return null;
          const result = new WebResourceResponse();
          result.setResponseData(url === appOrigin + '/' ? $rawfile('web/index.html') : $rawfile('restore.html'));
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
