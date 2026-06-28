// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package shared — общий код use-cases (filter parsing, pagination, update-mask
// helpers), не привязанный к одному ресурсу.
//
// ParseNameFilter — единый name=-парсер всех List use-cases (делегирует в
// kacho-corelib/filter.Parse, whitelist {"name"}).
//
// + (tracked): MaskApplier (general update_mask discipline), ListPaginator
// (cursor (created_at, id) base64 token).
package shared
