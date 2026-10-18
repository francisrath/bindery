### Fixed
- **A RAR release with a small notes file beside it now waits for an unpacker** (#3232). A release that shipped a tiny `.txt` note, or an extracted ebook under 4 KiB, next to its archive volumes counted as already extracted, so the import was blocked as too small before an unpacker could run. Files under the minimum ebook size no longer end the wait.
