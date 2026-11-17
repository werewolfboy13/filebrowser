import { fetchURL, removePrefix, createURL } from "./utils";
import { baseURL } from "@/utils/constants";
import { encodePath } from "@/utils/url";

export async function fetch(url: string, password: string = "") {
  url = removePrefix(url);

  const res = await fetchURL(
    `/api/public/share${url}`,
    {
      headers: { "X-SHARE-PASSWORD": encodeURIComponent(password) },
    },
    false
  );

  const data = (await res.json()) as Resource;
  data.url = `/share${url}`;

  if (data.isDir) {
    if (!data.url.endsWith("/")) data.url += "/";
    data.items = data.items.map((item: any, index: any) => {
      item.index = index;
      item.url = `${data.url}${encodeURIComponent(item.name)}`;

      if (item.isDir) {
        item.url += "/";
      }

      return item;
    });
  }

  return data;
}

export function download(
  format: DownloadFormat,
  hash: string,
  token: string,
  ...files: string[]
) {
  let url = `${baseURL}/api/public/dl/${encodeURIComponent(hash)}`;

  // Item paths are raw names: encode them (as api/files.ts does with its
  // pre-encoded URLs) so '#', '?', '%' and ',' (the files= separator)
  // survive. The server splits files= on ',' and unescapes each entry.
  if (files.length === 1) {
    url += encodePath(files[0]) + "?";
  } else {
    const arg = files.map((file) => encodePath(file)).join(",");
    url += `/?files=${encodeURIComponent(arg)}&`;
  }

  if (format) {
    url += `algo=${encodeURIComponent(format)}&`;
  }

  if (token) {
    url += `token=${encodeURIComponent(token)}&`;
  }

  window.open(url);
}

export function getDownloadURL(res: Resource, inline = false) {
  const params = {
    ...(inline && { inline: "true" }),
    ...(res.token && { token: res.token }),
  };

  return createURL("api/public/dl/" + res.hash + res.path, params);
}
