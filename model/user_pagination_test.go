package model

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func insertUsersForPaginationTest(t *testing.T, total int) {
	t.Helper()
	for id := 1; id <= total; id++ {
		user := &User{
			Id:          id,
			Username:    fmt.Sprintf("user%02d", id),
			Password:    "password123",
			DisplayName: fmt.Sprintf("User %02d", id),
			Email:       fmt.Sprintf("user%02d@example.com", id),
			Role:        common.RoleCommonUser,
			Status:      common.UserStatusEnabled,
			Group:       "default",
			AffCode:     fmt.Sprintf("aff%02d", id),
		}
		require.NoError(t, DB.Create(user).Error)
	}
}

func collectUserIDs(users []*User) []int {
	ids := make([]int, 0, len(users))
	for _, user := range users {
		ids = append(ids, user.Id)
	}
	return ids
}

func TestGetAllUsersSortsBeforePagination(t *testing.T) {
	truncateTables(t)
	insertUsersForPaginationTest(t, 42)

	pageOne, total, err := GetAllUsers(&common.PageInfo{Page: 1, PageSize: 20}, NewUserSortOptions("id", "asc"))
	require.NoError(t, err)
	assert.Equal(t, int64(42), total)
	assert.Equal(t, []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}, collectUserIDs(pageOne))

	pageTwo, total, err := GetAllUsers(&common.PageInfo{Page: 2, PageSize: 20}, NewUserSortOptions("id", "asc"))
	require.NoError(t, err)
	assert.Equal(t, int64(42), total)
	assert.Equal(t, []int{21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40}, collectUserIDs(pageTwo))

	pageThree, total, err := GetAllUsers(&common.PageInfo{Page: 3, PageSize: 20}, NewUserSortOptions("id", "asc"))
	require.NoError(t, err)
	assert.Equal(t, int64(42), total)
	assert.Equal(t, []int{41, 42}, collectUserIDs(pageThree))
}

func TestSearchUsersSortsBeforePagination(t *testing.T) {
	truncateTables(t)
	insertUsersForPaginationTest(t, 42)

	users, total, err := SearchUsers("user", "", nil, nil, 20, 20, NewUserSortOptions("id", "asc"))
	require.NoError(t, err)
	assert.Equal(t, int64(42), total)
	assert.Equal(t, []int{21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40}, collectUserIDs(users))
}

func TestGetAllUsersSortsMoneyColumnsBeforePagination(t *testing.T) {
	truncateTables(t)
	insertUsersForPaginationTest(t, 3)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 1).Updates(map[string]any{"quota": 100, "used_quota": 300}).Error)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 2).Updates(map[string]any{"quota": 300, "used_quota": 100}).Error)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 3).Updates(map[string]any{"quota": 200, "used_quota": 200}).Error)

	tests := []struct {
		name      string
		sortBy    string
		sortOrder string
		want      []int
	}{
		{name: "balance descending", sortBy: "quota", sortOrder: "desc", want: []int{2, 3, 1}},
		{name: "balance ascending", sortBy: "quota", sortOrder: "asc", want: []int{1, 3, 2}},
		{name: "consumed descending", sortBy: "used_quota", sortOrder: "desc", want: []int{1, 3, 2}},
		{name: "consumed ascending", sortBy: "used_quota", sortOrder: "asc", want: []int{2, 3, 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			users, total, err := GetAllUsers(&common.PageInfo{Page: 1, PageSize: 3}, NewUserSortOptions(test.sortBy, test.sortOrder))
			require.NoError(t, err)
			assert.Equal(t, int64(3), total)
			assert.Equal(t, test.want, collectUserIDs(users))
		})
	}
}

func TestGetAllUsersIncludesGoogleIdentity(t *testing.T) {
	truncateTables(t)
	insertUsersForPaginationTest(t, 2)
	provider := &CustomOAuthProvider{Name: "Google", Slug: "google", ClientId: "client", AuthorizationEndpoint: "https://accounts.example/authorize", TokenEndpoint: "https://accounts.example/token", UserInfoEndpoint: "https://accounts.example/userinfo"}
	require.NoError(t, DB.Create(provider).Error)
	require.NoError(t, DB.Create(&UserOAuthBinding{UserId: 2, ProviderId: provider.Id, ProviderUserId: "google-subject-2"}).Error)

	users, _, err := GetAllUsers(&common.PageInfo{Page: 1, PageSize: 20}, NewUserSortOptions("id", "asc"))
	require.NoError(t, err)
	assert.Empty(t, users[0].GoogleId)
	assert.Equal(t, "google-subject-2", users[1].GoogleId)
}
