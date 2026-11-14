/**
 * An upload as it goes on the wire: gzip-compressed when that makes it clearly smaller. The hub
 * unpacks it as it arrives (docs/API-board.md §4), so what lands is byte for byte the file.
 * Images, video, audio and archives are compressed already: gzip would cost time and save
 * nothing, so they go as they are.
 */
export interface Packed {
  body: Blob;
  encoding?: 'gzip';
  /** The size of the file itself. */
  size: number;
}

/** Below this, packing costs more than it saves. */
export const PACK_FROM = 16 * 1024;
/** Packed must be at most this share of the file, or the file goes as it is. */
const WORTH = 0.9;

const PACKED_EXT =
  /\.(png|jpe?g|gif|webp|avif|heic|heif|jxl|mp3|mp4|m4a|m4v|aac|ogg|oga|opus|flac|webm|mov|mkv|avi|wmv|zip|gz|tgz|bz2|xz|zst|7z|rar|jar|apk|aab|ipa|docx|xlsx|pptx|odt|ods|odp|epub|woff2?)$/i;
const PACKED_TYPE = /^(video|audio)\/|^image\/(png|jpeg|gif|webp|avif|heic|heif|jxl)$|^application\/(zip|gzip|x-7z|x-rar|x-xz|zstd|java-archive)/;

/** Whether gzip is worth trying on this file. */
export function worthPacking(file: Blob, name = (file as File).name ?? ''): boolean {
  if (file.size < PACK_FROM || typeof CompressionStream === 'undefined') return false;
  return !PACKED_EXT.test(name) && !PACKED_TYPE.test(file.type);
}

export async function packUpload(file: Blob, name?: string): Promise<Packed> {
  const plain: Packed = { body: file, size: file.size };
  if (!worthPacking(file, name)) return plain;
  try {
    // Blob to Blob through the browser's own gzip: no copy of the file in the page's memory
    const body = await new Response(file.stream().pipeThrough(new CompressionStream('gzip'))).blob();
    return body.size <= file.size * WORTH ? { body, encoding: 'gzip', size: file.size } : plain;
  } catch {
    return plain;
  }
}
