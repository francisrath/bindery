### Added
- **Set a book's cover through the API** (#3017). `POST /api/v1/book/{id}/cover` takes a JPEG, PNG, WebP or GIF image (up to 10 MB) as the request body and makes it the book's cover. The image is stored by Bindery itself, like covers taken from files, so it does not depend on a remote host. Refresh and enrichment only fill an empty cover, so an uploaded one stays.
