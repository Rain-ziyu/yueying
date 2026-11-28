import { abilityAccessCtrl, common } from '@kit.AbilityKit';
import { webview } from '@kit.ArkWeb';
@Entry
@Component
struct Room {
  controller: webview.WebviewController = new webview.WebviewController();
  build() {
    Column() {
      Web({ src: $rawfile('web/index.html'), controller: this.controller })
        .javaScriptAccess(true)
        .domStorageAccess(true)
        .onPermissionRequest(async (event) => {
          const request = event.request;
          const origin = request.getOrigin();
          const audio = ProtectedResourceType.AUDIO_CAPTURE;
          if ((!origin.startsWith('resource://') && !origin.startsWith('file://')) ||
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
