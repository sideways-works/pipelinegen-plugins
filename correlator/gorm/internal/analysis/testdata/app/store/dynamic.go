package store

import (
	"fmt"
	"strings"

	"example.com/app/models"
	"gorm.io/gorm"
)

// purgeFrom builds its SQL from a parameter every caller passes as a
// constant.
func purgeFrom(db *gorm.DB, table string) error {
	return db.Exec("DELETE FROM " + table + " WHERE created_at < now()").Error
}

// PurgeAll calls purgeFrom with each table it purges.
func PurgeAll(db *gorm.DB) error {
	if err := purgeFrom(db, "drafts"); err != nil {
		return err
	}

	return purgeFrom(db, "archive.old_drafts")
}

// Filter builds a condition piece by piece.
func Filter(db *gorm.DB, byAuthor bool) ([]models.Note, error) {
	where := "deleted_at IS NULL"
	if byAuthor {
		where += " AND author_id IN (SELECT id FROM authors WHERE active)"
	}

	var ns []models.Note
	err := db.Where(where).Find(&ns).Error

	return ns, err
}

// Joined builds a statement from a list of conditions.
func Joined(db *gorm.DB, tagged bool, limit int) ([]models.Note, error) {
	conds := []string{"body <> ''"}
	if tagged {
		conds = append(conds, "id IN (SELECT note_id FROM note_tags)")
	}

	var ns []models.Note
	err := db.Raw(fmt.Sprintf("SELECT * FROM notes WHERE %s LIMIT %d", strings.Join(conds, " AND "), limit)).Scan(&ns).Error

	return ns, err
}

func runSQL(db *gorm.DB, q string) error {
	return db.Exec(q).Error
}

// Indirect passes runSQL as a value, so its callers aren't all known.
func Indirect(db *gorm.DB) error {
	run := runSQL

	return run(db, "DELETE FROM drafts")
}
