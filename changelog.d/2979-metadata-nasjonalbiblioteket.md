### Added
- **Nasjonalbiblioteket metadata provider** (#2979). The National Library of Norway can be chosen as the primary metadata provider in Settings → Metadata Profiles. It covers Norwegian publications by legal deposit under their original Norwegian titles, so a Norwegian library's files match the author's catalogue instead of the English translations other providers list. Books carry the author's series and their number in it. It is opt in: an install that does not select it never contacts the National Library.

### Fixed
- **Norwegian language filter** (#2979). A metadata profile allowing Norwegian now accepts books tagged Bokmål or Nynorsk (`nob`, `nno`, `nb`, `nn`), which it previously rejected.
