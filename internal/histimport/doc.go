// Package histimport parses the history exports of other apps (TezLab, ABRP) into drive and charge candidates. It is pure: no database, no
// network, no logging of row values. The caller (the import API) does the dedupe against stored rows and the writes.
//
// The export formats are not documented anywhere public, so each source is a Format: a table of header aliases per field, built from the
// columns we assume and meant to be edited once real exports are in hand (slice 2b). The parser fails closed: a required field with no
// matching header refuses the file (MissingColumnsError, header names only), a unit or zone the file does not state is asked of the
// user (NeedChoiceError), a row that fails a check is skipped and counted by reason, and a file with more than 20% failing rows is
// refused (TooManyBadRowsError).
package histimport
