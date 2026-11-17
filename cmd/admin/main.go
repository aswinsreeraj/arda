package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/aswinsreeraj/arda/pkg/database"
	"github.com/aswinsreeraj/arda/services/gateway/auth"
	"github.com/aswinsreeraj/arda/services/gateway/identity"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	command := strings.ToLower(os.Args[1])

	db, err := database.Connect(database.LoadConfigFromEnv())
	if err != nil {
		log.Fatalf("Failed to connect to PostgreSQL: %v", err)
	}
	defer db.Close()

	store := identity.NewPostgresStore(db.DB)
	ctx := context.Background()

	switch command {
	case "list":
		handleList(ctx, db)

	case "promote":
		if len(os.Args) < 3 {
			fmt.Println("Usage: arda-admin promote <email>")
			os.Exit(1)
		}
		email := strings.ToLower(strings.TrimSpace(os.Args[2]))
		handlePromote(ctx, store, db, email)

	case "demote":
		if len(os.Args) < 3 {
			fmt.Println("Usage: arda-admin demote <email>")
			os.Exit(1)
		}
		email := strings.ToLower(strings.TrimSpace(os.Args[2]))
		handleDemote(ctx, store, db, email)

	case "passwd":
		if len(os.Args) < 4 {
			fmt.Println("Usage: arda-admin passwd <email> <new-password>")
			os.Exit(1)
		}
		email := strings.ToLower(strings.TrimSpace(os.Args[2]))
		newPass := os.Args[3]
		handlePasswd(ctx, db, email, newPass)

	case "create":
		if len(os.Args) < 4 {
			fmt.Println("Usage: arda-admin create <email> <password> [tenant-name]")
			os.Exit(1)
		}
		email := strings.ToLower(strings.TrimSpace(os.Args[2]))
		password := os.Args[3]
		tenantName := "ARDA Platform"
		if len(os.Args) >= 5 {
			tenantName = os.Args[4]
		}
		handleCreate(ctx, store, db, email, password, tenantName)

	default:
		fmt.Printf("Unknown command: %s\n", command)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("ARDA Platform Super Admin Management CLI")
	fmt.Println("\nCommands:")
	fmt.Println("  list                              List all users and their super_admin status")
	fmt.Println("  promote <email>                   Promote an existing user to super_admin")
	fmt.Println("  demote <email>                    Demote a super_admin to regular user")
	fmt.Println("  passwd <email> <new-password>     Set user password")
	fmt.Println("  create <email> <password> [org]   Create a new super_admin account")
}

func handleList(ctx context.Context, db *database.DB) {
	type userRow struct {
		ID           string    `gorm:"column:id"`
		Email        string    `gorm:"column:email"`
		IsSuperAdmin bool      `gorm:"column:is_super_admin"`
		Status       string    `gorm:"column:status"`
		CreatedAt    time.Time `gorm:"column:created_at"`
	}

	var users []userRow
	if err := db.DB.WithContext(ctx).Table("app_users").Order("created_at ASC").Find(&users).Error; err != nil {
		log.Fatalf("Failed to query app_users: %v", err)
	}

	fmt.Printf("%-38s %-30s %-14s %-10s\n", "USER ID", "EMAIL", "SUPER ADMIN", "STATUS")
	fmt.Println(strings.Repeat("-", 96))
	for _, u := range users {
		superTag := "No"
		if u.IsSuperAdmin {
			superTag = "YES (Platform)"
		}
		fmt.Printf("%-38s %-30s %-14s %-10s\n", u.ID, u.Email, superTag, u.Status)
	}
}

func handlePromote(ctx context.Context, store identity.Store, db *database.DB, email string) {
	user, err := store.GetUserByEmail(ctx, email)
	if err != nil {
		log.Fatalf("User with email %q not found: %v", email, err)
	}

	now := time.Now().UTC()
	err = db.DB.WithContext(ctx).Table("app_users").
		Where("id = ?", user.ID).
		Updates(map[string]any{
			"is_super_admin":    true,
			"status":            identity.UserStatusActive,
			"email_verified_at": now,
			"updated_at":        now,
		}).Error
	if err != nil {
		log.Fatalf("Failed to promote user %s: %v", email, err)
	}

	fmt.Printf("Successfully promoted %s (%s) to ARDA Super Admin!\n", email, user.ID)
	fmt.Println("Next time this user logs in, they will receive the 'super_admin' role and can access the /platform console.")
}

func handleDemote(ctx context.Context, store identity.Store, db *database.DB, email string) {
	user, err := store.GetUserByEmail(ctx, email)
	if err != nil {
		log.Fatalf("User with email %q not found: %v", email, err)
	}

	err = db.DB.WithContext(ctx).Table("app_users").
		Where("id = ?", user.ID).
		Update("is_super_admin", false).Error
	if err != nil {
		log.Fatalf("Failed to demote user %s: %v", email, err)
	}

	fmt.Printf("Successfully demoted %s (%s). They are no longer a super_admin.\n", email, user.ID)
}

func handleCreate(ctx context.Context, store identity.Store, db *database.DB, email, password, tenantName string) {
	existing, _ := store.GetUserByEmail(ctx, email)
	if existing != nil {
		fmt.Printf("User %s already exists. Promoting to super_admin instead...\n", email)
		handlePromote(ctx, store, db, email)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		log.Fatalf("Failed to hash password: %v", err)
	}

	now := time.Now().UTC()
	userID := uuid.New()
	tenantID := uuid.New()

	// 1. Create or ensure platform tenant
	var existingTenant struct {
		ID uuid.UUID `gorm:"column:id"`
	}
	err = db.DB.WithContext(ctx).Table("tenants").Where("name = ?", tenantName).First(&existingTenant).Error
	if err == nil && existingTenant.ID != uuid.Nil {
		tenantID = existingTenant.ID
	} else {
		err = db.DB.WithContext(ctx).Table("tenants").Create(map[string]any{
			"id":         tenantID,
			"name":       tenantName,
			"status":     "active",
			"created_at": now,
			"updated_at": now,
		}).Error
		if err != nil {
			log.Fatalf("Failed to create tenant: %v", err)
		}
	}

	// 2. Create user
	err = db.DB.WithContext(ctx).Table("app_users").Create(map[string]any{
		"id":                userID,
		"email":             email,
		"display_name":      "Super Admin",
		"password_hash":     string(hash),
		"is_super_admin":    true,
		"status":            identity.UserStatusActive,
		"email_verified_at": now,
		"created_at":        now,
		"updated_at":        now,
	}).Error
	if err != nil {
		log.Fatalf("Failed to create super admin user: %v", err)
	}

	// 3. Create active membership in tenant
	memID := uuid.New()
	err = db.DB.WithContext(ctx).Table("tenant_memberships").Create(map[string]any{
		"id":         memID,
		"tenant_id":  tenantID,
		"user_id":    userID,
		"role":       string(auth.RoleCompanyAdmin),
		"status":     string(identity.MembershipStatusActive),
		"created_at": now,
		"updated_at": now,
	}).Error
	if err != nil {
		log.Fatalf("Failed to create tenant membership: %v", err)
	}

	fmt.Printf("Successfully created Super Admin user %s!\n", email)
	fmt.Printf("User ID:   %s\n", userID)
	fmt.Printf("Tenant:    %s (%s)\n", tenantName, tenantID)
	fmt.Println("You can now log in using this email and password to access the /platform Super Admin console.")
}

func handlePasswd(ctx context.Context, db *database.DB, email, newPassword string) {
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		log.Fatalf("Failed to hash password: %v", err)
	}

	res := db.DB.WithContext(ctx).Table("app_users").
		Where("LOWER(email) = ?", email).
		Update("password_hash", string(hash))
	if res.Error != nil {
		log.Fatalf("Failed to update password for %s: %v", email, res.Error)
	}
	if res.RowsAffected == 0 {
		log.Fatalf("User %s not found", email)
	}

	fmt.Printf("Successfully updated password for %s\n", email)
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
