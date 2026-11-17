/**
 * Label shown in the captions menu for a subtitle track: the subtitle file
 * name without its extension.
 */
export function subLabel(subUrl: string): string {
  let url: URL;
  try {
    url = new URL(subUrl);
  } catch {
    // treat it as a relative url
    // we only need this for filename
    url = new URL(subUrl, window.location.origin);
  }

  return decodeURIComponent(
    url.pathname
      .split("/")
      .pop()!
      .replace(/\.[^/.]+$/, "")
  );
}
