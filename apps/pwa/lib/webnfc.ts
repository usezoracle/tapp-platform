// Thin wrapper over the Web NFC API.
//
// Browser support: Chrome on Android only. iOS Safari has no
// `navigator.nfc` at all — callers should gate UI on
// `webNfcSupported()` and surface the iOS escape-hatch copy described
// in `tapp/docs/resync-flow.md`.
//
// Web NFC only exposes the NDEF logical layer — there's no raw
// PWD_AUTH or page-level access. That shapes the on-card data model:
// K + rotation token both live inside the NDEF payload (see
// `cardCrypto.ts`). The merchant app on real hardware can do
// PWD_AUTH on top, but v1's PWA-first cards can't, so we don't rely
// on it.

const ZORACLE_NDEF_TYPE = "usetapp.xyz:tapp-card";

export function webNfcSupported(): boolean {
  return typeof window !== "undefined" && "NDEFReader" in window;
}

/** Read the first matching Zoracle NDEF record off a tapped card. */
export async function readCardPayload(signal?: AbortSignal): Promise<{
  uid: Uint8Array;
  payload: Uint8Array;
}> {
  assertSupported();
  const reader = new (window as unknown as { NDEFReader: NDEFReaderCtor }).NDEFReader();
  await reader.scan({ signal });
  return await new Promise((resolve, reject) => {
    reader.onreadingerror = () => reject(new Error("NFC read failed"));
    reader.onreading = (event: NDEFReadingEvent) => {
      try {
        // External-type records report their type string in `recordType`
        // (there is no `mediaType` for them — that's mime-only).
        const record = event.message.records.find(
          (r) => r.recordType === ZORACLE_NDEF_TYPE,
        );
        if (!record || !record.data) {
          reject(new Error("Card has no Freedom payload — needs to be linked first"));
          return;
        }
        const data = record.data;
        let payload: Uint8Array;
        if (data instanceof DataView) {
          payload = new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
        } else if (data instanceof ArrayBuffer) {
          payload = new Uint8Array(data);
        } else {
          payload = new Uint8Array(data);
        }
        resolve({
          uid: serialNumberToBytes(event.serialNumber),
          payload,
        });
      } catch (err) {
        reject(err instanceof Error ? err : new Error("Bad NFC read"));
      }
    };
  });
}

/**
 * Write a Zoracle external-type NDEF record to the next-tapped card.
 * Used both at linking (initial K + token payload) and on resync.
 */
export async function writeCardPayload(payload: Uint8Array, signal?: AbortSignal): Promise<void> {
  assertSupported();
  const reader = new (window as unknown as { NDEFReader: NDEFReaderCtor }).NDEFReader();
  await reader.write(
    {
      records: [
        {
          // External-type record: the "<domain>:<type>" string IS the
          // recordType. `mediaType` is mime-only and throws here if set.
          recordType: ZORACLE_NDEF_TYPE,
          // Web NFC expects ArrayBuffer or string for `data`; pass
          // the underlying buffer so a 0x00 byte doesn't get
          // truncated as a string terminator.
          // Slice the underlying buffer to an ArrayBuffer the Web NFC
          // spec accepts. Cast to ArrayBuffer since TS's `slice`
          // return type widens to SharedArrayBuffer in env libs.
          data: payload.buffer.slice(
            payload.byteOffset,
            payload.byteOffset + payload.byteLength,
          ) as ArrayBuffer,
        },
      ],
    },
    { signal },
  );
}

/**
 * Convenience: `K || rotation_token` is the canonical Zoracle
 * payload shape — 32 bytes secret + 32 bytes rotation token = 64 bytes.
 */
export function packCardPayload(K: Uint8Array, rotationToken: Uint8Array): Uint8Array {
  if (K.length !== 32) throw new Error("K must be 32 bytes");
  if (rotationToken.length !== 32) throw new Error("rotation token must be 32 bytes");
  const out = new Uint8Array(64);
  out.set(K, 0);
  out.set(rotationToken, 32);
  return out;
}

/** Inverse of `packCardPayload`. */
export function unpackCardPayload(payload: Uint8Array): {
  K: Uint8Array;
  rotationToken: Uint8Array;
} {
  if (payload.length !== 64) {
    throw new Error(`Expected 64-byte payload, got ${payload.length}`);
  }
  return {
    K: payload.slice(0, 32),
    rotationToken: payload.slice(32, 64),
  };
}

// -----------------------------------------------------------------------------

function assertSupported() {
  if (!webNfcSupported()) {
    throw new Error(
      "Web NFC isn't available in this browser. Use Chrome on Android — or " +
        "contact support for the iOS recovery flow.",
    );
  }
}

function serialNumberToBytes(sn: string): Uint8Array {
  // Web NFC reports the UID as a colon-separated hex string, e.g.
  // "04:8c:32:9a:b1:23:80". Strip colons + hex-decode.
  const clean = sn.replace(/[^0-9a-fA-F]/g, "");
  const out = new Uint8Array(clean.length / 2);
  for (let i = 0; i < out.length; i++) {
    out[i] = Number.parseInt(clean.substring(i * 2, i * 2 + 2), 16);
  }
  return out;
}

// -----------------------------------------------------------------------------
// Minimal NDEFReader type defs — TypeScript's `lib.dom` doesn't ship
// Web NFC typings yet (Stage 4 draft). Narrow to what we use here.

// Web NFC's `data` field accepts BufferSource | string. We narrow to
// the shapes we actually emit (ArrayBuffer at write time, DataView at
// read time per the spec).
interface NDEFRecord {
  recordType: string;
  mediaType?: string;
  data?: DataView | ArrayBuffer | Uint8Array;
}
interface NDEFMessage {
  records: NDEFRecord[];
}
interface NDEFReadingEvent extends Event {
  serialNumber: string;
  message: NDEFMessage;
}
interface NDEFReaderInstance {
  scan(opts?: { signal?: AbortSignal }): Promise<void>;
  write(message: { records: NDEFRecord[] }, opts?: { signal?: AbortSignal }): Promise<void>;
  onreading: ((event: NDEFReadingEvent) => void) | null;
  onreadingerror: (() => void) | null;
}
type NDEFReaderCtor = new () => NDEFReaderInstance;
