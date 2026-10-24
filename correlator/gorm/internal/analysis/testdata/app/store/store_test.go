package store

import "gorm.io/gorm"

func seed(db *gorm.DB) error {
	return db.Exec("INSERT INTO labels (label) VALUES ('first')").Error
}
