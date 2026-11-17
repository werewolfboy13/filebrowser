// Options of vue-lazyload (the listing's thumbnails). dispatchEvent makes
// it fire an "error" event on the <img> when a thumbnail cannot be loaded
// (e.g. the server refuses to make a preview of a very large image), so
// that ListingItem can show the file's icon instead of a blank image.
export const lazyloadOptions = {
  dispatchEvent: true,
};
