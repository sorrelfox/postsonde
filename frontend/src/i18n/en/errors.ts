// Messages of errors from Go, by error code (docs/errors.md), for locales
// other than English. See docs/frontend.md#localization.
//
// Go writes its error messages and hints in English, and they are the
// English text: this file stays empty. A locale that translates them
// keys its own errors messages by code, "<code>" for the message and
// "<code>.hint" for the hint, with the error's details as placeholders:
//
//   "settings.invalid_value": "{field}: {value} n'est pas valide",
//
// toAppError (lib/errors.ts) uses them in place of Go's text when the
// locale in use has the code, and Go's English text otherwise.
import type { ErrorMessages } from "../catalog";

export default {} satisfies ErrorMessages as ErrorMessages;
