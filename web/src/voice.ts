import { ref, watch, type ComputedRef } from "vue";
import processor from "./microphone.js?raw";

export function useRoomVoice(
  send: (data: string) => void,
  allowed: ComputedRef<boolean>,
) {
  const mic = ref(false),
    pending = ref(false),
    speaker = ref(true);
  const hint = ref(""),
    needsGesture = ref(false);
  let context: AudioContext | undefined, output: GainNode | undefined;
  let stream: MediaStream | undefined, capture: AudioWorkletNode | undefined;
  let loaded: Promise<void> | undefined,
    generation = 0,
    wanted = true;
  const nextFrame = new Map<string, number>();

  function audio() {
    if (!context) {
      context = new AudioContext({ sampleRate: 16000 });
      output = context.createGain();
      output.connect(context.destination);
      output.gain.value = Number(speaker.value);
      const current = context;
      current.onstatechange = () => {
        if (context === current)
          needsGesture.value = current.state !== "running";
      };
      needsGesture.value = current.state !== "running";
    }
    return context;
  }
  function stopMic() {
    generation++;
    capture?.disconnect();
    if (capture) capture.port.onmessage = null;
    capture = undefined;
    stream?.getTracks().forEach((track) => track.stop());
    stream = undefined;
    mic.value = pending.value = false;
  }
  async function startMic() {
    const attempt = ++generation;
    pending.value = true;
    hint.value = "";
    try {
      const ctx = audio();
      void ctx.resume().catch(() => {});
      if (!navigator.mediaDevices?.getUserMedia || !ctx.audioWorklet)
        throw new Error("unsupported");
      const input = await navigator.mediaDevices.getUserMedia({
        audio: {
          channelCount: 1,
          echoCancellation: true,
          noiseSuppression: true,
        },
      });
      if (generation !== attempt) {
        input.getTracks().forEach((track) => track.stop());
        return;
      }
      stream = input;
      if (!loaded) {
        const url = URL.createObjectURL(
          new Blob([processor], { type: "text/javascript" }),
        );
        loaded = ctx.audioWorklet
          .addModule(url)
          .finally(() => URL.revokeObjectURL(url));
      }
      await loaded;
      if (generation !== attempt) return;
      capture = new AudioWorkletNode(ctx, "room-microphone");
      ctx.createMediaStreamSource(input).connect(capture);
      capture.connect(ctx.destination); // The worklet produces silence locally.
      capture.port.onmessage = ({ data }) => {
        if (attempt === generation && allowed.value)
          send(btoa(String.fromCharCode(...new Uint8Array(data))));
      };
      input.getAudioTracks()[0].onended = () => {
        if (attempt === generation) {
          stopMic();
          hint.value = "麦克风已断开";
        }
      };
      mic.value = true;
      pending.value = false;
    } catch (cause) {
      if (attempt !== generation) return;
      stopMic();
      loaded = undefined;
      wanted = false;
      hint.value =
        cause instanceof DOMException && cause.name === "NotAllowedError"
          ? "麦克风未获授权，可在设备权限设置中开启"
          : "麦克风不可用，请检查设备及安全连接";
    }
  }
  watch(
    allowed,
    (value) => {
      if (value && wanted) void startMic();
      else stopMic();
    },
    { flush: "sync" },
  );

  function toggleMic() {
    wanted = !(mic.value || pending.value);
    if (wanted && allowed.value) void startMic();
    else stopMic();
  }
  function resumeAudio() {
    void audio()
      .resume()
      .catch(() => {
        hint.value = "设备未能启动语音播放";
      });
  }
  function toggleSpeaker() {
    audio();
    speaker.value = !speaker.value;
    output!.gain.value = Number(speaker.value);
    if (speaker.value) resumeAudio();
  }
  function receive(from: string, data: string) {
    if (!speaker.value) return;
    const ctx = audio();
    if (ctx.state !== "running") return;
    const pcm = new Int16Array(
      Uint8Array.from(atob(data), (c) => c.charCodeAt(0)).buffer,
    );
    const buffer = ctx.createBuffer(1, pcm.length, 16000);
    const samples = buffer.getChannelData(0);
    pcm.forEach((v, i) => {
      samples[i] = v / 32768;
    });
    const now = ctx.currentTime;
    const start = Math.max(now + 0.012, nextFrame.get(from) ?? 0);
    if (start > now + 0.24) return;
    const source = ctx.createBufferSource();
    source.buffer = buffer;
    source.connect(output!);
    const end = start + buffer.duration;
    nextFrame.set(from, end);
    source.onended = () => {
      if (nextFrame.get(from) === end) nextFrame.delete(from);
    };
    source.start(start);
  }
  function disconnectVoice() {
    stopMic();
    void context?.close().catch(() => {});
    context = output = loaded = undefined;
    nextFrame.clear();
    needsGesture.value = false;
  }
  function resetVoice() {
    disconnectVoice();
    wanted = speaker.value = true;
    hint.value = "";
  }
  return {
    mic,
    pending,
    speaker,
    needsGesture,
    hint,
    toggleMic,
    toggleSpeaker,
    resumeAudio,
    receive,
    disconnectVoice,
    resetVoice,
  };
}
