// Package shared — общий код use-cases (filter parsing, pagination, update-mask
// helpers), не привязанный к одному ресурсу.
//
// TODO(KAC-153+): MaskApplier (general update_mask discipline), ListPaginator
// (cursor (created_at, id) base64 token), filter.Parse whitelist per resource.
package shared
