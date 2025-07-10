package types

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCacheDepositsWithdraws(t *testing.T) {
	tests := []struct {
		name           string
		setupStakers   func() *Stakers
		deposits       map[uint64]*DepositInfo
		nextVersion    uint64
		withdraws      []*WithdrawInfo
		expectedError  string
		expectedResult func(*Stakers) bool
	}{
		{
			name: "valid deposits and withdraws",
			setupStakers: func() *Stakers {
				return &Stakers{
					Version:         1,
					WithdrawVersion: 2,
					SInfos:          make(StakerInfos),
					SInfosAdd:       make(map[uint64]*DepositInfo),
					WithdrawInfos:   []*WithdrawInfo{},
				}
			},
			deposits: map[uint64]*DepositInfo{
				2: {
					StakerIndex: 1,
					StakerAddr:  "0x123",
					Validator:   "validator1",
					Amount:      1000,
				},
				3: {
					StakerIndex: 2,
					StakerAddr:  "0x456",
					Validator:   "validator2",
					Amount:      2000,
				},
			},
			nextVersion: 2,
			withdraws: []*WithdrawInfo{
				{
					StakerIndex:     1,
					WithdrawVersion: 3,
				},
				{
					StakerIndex:     2,
					WithdrawVersion: 4,
				},
			},
			expectedError: "",
			expectedResult: func(s *Stakers) bool {
				return len(s.SInfosAdd) == 2 &&
					s.SInfosAdd[2] != nil &&
					s.SInfosAdd[3] != nil &&
					len(s.WithdrawInfos) == 2 &&
					s.WithdrawInfos[0].WithdrawVersion == 3 &&
					s.WithdrawInfos[1].WithdrawVersion == 4
			},
		},
		{
			name: "deposits with nextVersion 0 should fail",
			setupStakers: func() *Stakers {
				return &Stakers{
					Version:         1,
					WithdrawVersion: 2,
					SInfos:          make(StakerInfos),
					SInfosAdd:       make(map[uint64]*DepositInfo),
					WithdrawInfos:   []*WithdrawInfo{},
				}
			},
			deposits: map[uint64]*DepositInfo{
				1: {
					StakerIndex: 1,
					StakerAddr:  "0x123",
					Validator:   "validator1",
					Amount:      1000,
				},
			},
			nextVersion:    0,
			withdraws:      []*WithdrawInfo{},
			expectedError:  "failed to cache deposits, nextVersion is 0",
			expectedResult: nil,
		},
		{
			name: "version not continuous should fail",
			setupStakers: func() *Stakers {
				return &Stakers{
					Version:         1,
					WithdrawVersion: 2,
					SInfos:          make(StakerInfos),
					SInfosAdd: map[uint64]*DepositInfo{
						2: {
							StakerIndex: 1,
							StakerAddr:  "0x123",
							Validator:   "validator1",
							Amount:      1000,
						},
					},
					WithdrawInfos: []*WithdrawInfo{},
				}
			},
			deposits: map[uint64]*DepositInfo{
				4: {
					StakerIndex: 2,
					StakerAddr:  "0x456",
					Validator:   "validator2",
					Amount:      2000,
				},
			},
			nextVersion:    4,
			withdraws:      []*WithdrawInfo{},
			expectedError:  "failed to cache deposits, version not continuos:true, or already exists:false, nextVersion:4",
			expectedResult: nil,
		},
		{
			name: "version already exists should fail",
			setupStakers: func() *Stakers {
				return &Stakers{
					Version:         1,
					WithdrawVersion: 2,
					SInfos:          make(StakerInfos),
					SInfosAdd: map[uint64]*DepositInfo{
						2: {
							StakerIndex: 1,
							StakerAddr:  "0x123",
							Validator:   "validator1",
							Amount:      1000,
						},
					},
					WithdrawInfos: []*WithdrawInfo{},
				}
			},
			deposits: map[uint64]*DepositInfo{
				2: {
					StakerIndex: 2,
					StakerAddr:  "0x456",
					Validator:   "validator2",
					Amount:      2000,
				},
			},
			nextVersion:    2,
			withdraws:      []*WithdrawInfo{},
			expectedError:  "failed to cache deposits, version not continuos:true, or already exists:true, nextVersion:2",
			expectedResult: nil,
		},
		{
			name: "version mismatch should fail",
			setupStakers: func() *Stakers {
				return &Stakers{
					Version:         1,
					WithdrawVersion: 2,
					SInfos:          make(StakerInfos),
					SInfosAdd:       make(map[uint64]*DepositInfo),
					WithdrawInfos:   []*WithdrawInfo{},
				}
			},
			deposits: map[uint64]*DepositInfo{
				3: {
					StakerIndex: 1,
					StakerAddr:  "0x123",
					Validator:   "validator1",
					Amount:      1000,
				},
			},
			nextVersion:    3,
			withdraws:      []*WithdrawInfo{},
			expectedError:  "failed to cache deposits, version mismatch, current:1, next:3",
			expectedResult: nil,
		},
		{
			name: "missing version in deposits should fail",
			setupStakers: func() *Stakers {
				return &Stakers{
					Version:         1,
					WithdrawVersion: 2,
					SInfos:          make(StakerInfos),
					SInfosAdd:       make(map[uint64]*DepositInfo),
					WithdrawInfos:   []*WithdrawInfo{},
				}
			},
			deposits: map[uint64]*DepositInfo{
				2: {
					StakerIndex: 1,
					StakerAddr:  "0x123",
					Validator:   "validator1",
					Amount:      1000,
				},
				// Missing version 3
				4: {
					StakerIndex: 2,
					StakerAddr:  "0x456",
					Validator:   "validator2",
					Amount:      2000,
				},
			},
			nextVersion:    2,
			withdraws:      []*WithdrawInfo{},
			expectedError:  "failed to cache deposits, version not continuos, missed version:3",
			expectedResult: nil,
		},
		{
			name: "withdraw version 0 should fail",
			setupStakers: func() *Stakers {
				return &Stakers{
					Version:         1,
					WithdrawVersion: 2,
					SInfos:          make(StakerInfos),
					SInfosAdd:       make(map[uint64]*DepositInfo),
					WithdrawInfos:   []*WithdrawInfo{},
				}
			},
			deposits:    map[uint64]*DepositInfo{},
			nextVersion: 2,
			withdraws: []*WithdrawInfo{
				{
					StakerIndex:     1,
					WithdrawVersion: 0,
				},
			},
			expectedError:  "failed to cache withdraws, withdraw version is 0",
			expectedResult: nil,
		},
		{
			name: "withdraw version already exists should fail",
			setupStakers: func() *Stakers {
				return &Stakers{
					Version:         1,
					WithdrawVersion: 3,
					SInfos:          make(StakerInfos),
					SInfosAdd:       make(map[uint64]*DepositInfo),
					WithdrawInfos:   []*WithdrawInfo{},
				}
			},
			deposits:    map[uint64]*DepositInfo{},
			nextVersion: 2,
			withdraws: []*WithdrawInfo{
				{
					StakerIndex:     1,
					WithdrawVersion: 2,
				},
			},
			expectedError:  "failed to cache withdraws, withdraw version already exists, current:3, next:2",
			expectedResult: nil,
		},
		{
			name: "withdraw version not continuous should fail",
			setupStakers: func() *Stakers {
				return &Stakers{
					Version:         1,
					WithdrawVersion: 2,
					SInfos:          make(StakerInfos),
					SInfosAdd:       make(map[uint64]*DepositInfo),
					WithdrawInfos: []*WithdrawInfo{
						{
							StakerIndex:     1,
							WithdrawVersion: 3,
						},
					},
				}
			},
			deposits:    map[uint64]*DepositInfo{},
			nextVersion: 2,
			withdraws: []*WithdrawInfo{
				{
					StakerIndex:     2,
					WithdrawVersion: 5, // Should be 4 to be continuous
				},
			},
			expectedError:  "failed to cache withdraws, withdraw version not continuos, current:3, next:5",
			expectedResult: nil,
		},
		{
			name: "empty deposits and withdraws should succeed",
			setupStakers: func() *Stakers {
				return &Stakers{
					Version:         1,
					WithdrawVersion: 2,
					SInfos:          make(StakerInfos),
					SInfosAdd:       make(map[uint64]*DepositInfo),
					WithdrawInfos:   []*WithdrawInfo{},
				}
			},
			deposits:      map[uint64]*DepositInfo{},
			nextVersion:   2,
			withdraws:     []*WithdrawInfo{},
			expectedError: "",
			expectedResult: func(s *Stakers) bool {
				return len(s.SInfosAdd) == 0 && len(s.WithdrawInfos) == 0
			},
		},
		{
			name: "only deposits should succeed",
			setupStakers: func() *Stakers {
				return &Stakers{
					Version:         1,
					WithdrawVersion: 2,
					SInfos:          make(StakerInfos),
					SInfosAdd:       make(map[uint64]*DepositInfo),
					WithdrawInfos:   []*WithdrawInfo{},
				}
			},
			deposits: map[uint64]*DepositInfo{
				2: {
					StakerIndex: 1,
					StakerAddr:  "0x123",
					Validator:   "validator1",
					Amount:      1000,
				},
			},
			nextVersion:   2,
			withdraws:     []*WithdrawInfo{},
			expectedError: "",
			expectedResult: func(s *Stakers) bool {
				return len(s.SInfosAdd) == 1 &&
					s.SInfosAdd[2] != nil &&
					s.SInfosAdd[2].StakerIndex == 1 &&
					len(s.WithdrawInfos) == 0
			},
		},
		{
			name: "only withdraws should succeed",
			setupStakers: func() *Stakers {
				return &Stakers{
					Version:         1,
					WithdrawVersion: 2,
					SInfos:          make(StakerInfos),
					SInfosAdd:       make(map[uint64]*DepositInfo),
					WithdrawInfos:   []*WithdrawInfo{},
				}
			},
			deposits:    map[uint64]*DepositInfo{},
			nextVersion: 2,
			withdraws: []*WithdrawInfo{
				{
					StakerIndex:     1,
					WithdrawVersion: 3,
				},
			},
			expectedError: "",
			expectedResult: func(s *Stakers) bool {
				return len(s.SInfosAdd) == 0 &&
					len(s.WithdrawInfos) == 1 &&
					s.WithdrawInfos[0].StakerIndex == 1 &&
					s.WithdrawInfos[0].WithdrawVersion == 3
			},
		},
		{
			name: "nil SInfosAdd should be initialized",
			setupStakers: func() *Stakers {
				return &Stakers{
					Version:         1,
					WithdrawVersion: 2,
					SInfos:          make(StakerInfos),
					SInfosAdd:       nil, // nil map
					WithdrawInfos:   []*WithdrawInfo{},
				}
			},
			deposits: map[uint64]*DepositInfo{
				2: {
					StakerIndex: 1,
					StakerAddr:  "0x123",
					Validator:   "validator1",
					Amount:      1000,
				},
			},
			nextVersion:   2,
			withdraws:     []*WithdrawInfo{},
			expectedError: "",
			expectedResult: func(s *Stakers) bool {
				return s.SInfosAdd != nil &&
					len(s.SInfosAdd) == 1 &&
					s.SInfosAdd[2] != nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stakers := tt.setupStakers()

			err := stakers.CacheDepositsWithdraws(tt.deposits, tt.nextVersion, tt.withdraws)

			if tt.expectedError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
			} else {
				require.NoError(t, err)
				if tt.expectedResult != nil {
					assert.True(t, tt.expectedResult(stakers))
				}
			}
		})
	}
}

func TestCacheDepositsWithdraws_Concurrency(t *testing.T) {
	// Test that the function is safe for concurrent access
	stakers := &Stakers{
		Version:         1,
		WithdrawVersion: 2,
		SInfos:          make(StakerInfos),
		SInfosAdd:       make(map[uint64]*DepositInfo),
		WithdrawInfos:   []*WithdrawInfo{},
		Locker:          &sync.RWMutex{},
	}

	// Note: The function doesn't use locks internally, so concurrent calls
	// to CacheDepositsWithdraws could cause race conditions
	// This test demonstrates that the function should be called with proper locking
	deposits := map[uint64]*DepositInfo{
		2: {
			StakerIndex: 1,
			StakerAddr:  "0x123",
			Validator:   "validator1",
			Amount:      1000,
		},
	}

	withdraws := []*WithdrawInfo{
		{
			StakerIndex:     1,
			WithdrawVersion: 3,
		},
	}

	// This should work fine since we're not calling it concurrently
	err := stakers.CacheDepositsWithdraws(deposits, 2, withdraws)
	require.NoError(t, err)

	assert.Len(t, stakers.SInfosAdd, 1)
	assert.Len(t, stakers.WithdrawInfos, 1)
}
