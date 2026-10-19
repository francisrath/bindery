import type { TFunction } from 'i18next'

/**
 * The message for a failed author save. A 409 means the author changed between
 * the page reading it and the save, typically a scheduled profile refresh
 * (#3287): the save was refused rather than overwrite that refresh, so the
 * user is told to reload and try again, in their language rather than the
 * server's English. Anything else shows the server's own message.
 */
export function authorSaveError(e: unknown, t: TFunction, fallback: string): string {
  // Checked by status rather than instanceof ApiError, so any error carrying
  // an HTTP status (ApiError, or a test double of it) is read the same way.
  if (e instanceof Error && (e as { status?: unknown }).status === 409) {
    return t('authorDetail.saveConflict', 'This author changed while you were saving. Reload the page and try again.')
  }
  return e instanceof Error ? e.message : fallback
}
