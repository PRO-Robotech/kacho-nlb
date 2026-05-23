// Package migrator — бизнес-логика мигратора kacho-nlb (отдельный binary cmd/migrator).
//
// TODO(KAC-148): реализация Dialect interface ({Up, Down, Status, Create}) + postgres
// adapter поверх goose + embedded FS из internal/migrations/.
package migrator
