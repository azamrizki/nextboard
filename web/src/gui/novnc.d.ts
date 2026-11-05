// The part of noVNC's RFB class the viewer uses (the package ships no types).
declare module '@novnc/novnc' {
  export default class RFB extends EventTarget {
    /** The protocol messages noVNC sends, replaceable to steer what the server repaints. */
    static messages: {
      fbUpdateRequest(sock: unknown, incremental: boolean, x: number, y: number, w: number, h: number): void;
      enableContinuousUpdates(sock: unknown, enable: boolean, x: number, y: number, w: number, h: number): void;
      clientEncodings(sock: unknown, encodings: number[]): void;
      extendedClipboardProvide(sock: unknown, formats: number[], data: string[]): void;
    };
    constructor(target: HTMLElement, url: string, options?: { shared?: boolean; wsProtocols?: string[] });
    scaleViewport: boolean;
    resizeSession: boolean;
    background: string;
    qualityLevel: number;
    compressionLevel: number;
    disconnect(): void;
    focus(options?: FocusOptions): void;
    clipboardPasteFrom(text: string): void;
    sendKey(keysym: number, code: string | null, down?: boolean): void;
  }
}
