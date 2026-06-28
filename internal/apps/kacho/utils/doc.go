// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package utils — мелкие helper'ы (TruncateID, logger-formatters, и т.п.),
// которые не вписываются в domain / repo / handler / clients.
//
// Helper'ы добавляются по мере возникновения; держать ≤1 файла на helper
// и явно не дублировать corelib (если nlb-only — здесь, иначе — в corelib).
package utils
