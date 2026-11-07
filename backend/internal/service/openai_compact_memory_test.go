package service

import (
	"bytes"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompactionNormalizationLargeOrdinaryRequestReusesPayload(t *testing.T) {
	body := largeNativeResponsesBody(2 << 20)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	out, changed, err := NormalizeCompactionTriggerInputOrder(body)
	runtime.ReadMemStats(&after)
	require.NoError(t, err)
	require.False(t, changed)
	require.Same(t, &body[0], &out[0])
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(1<<20), "普通图片请求的压缩标记检查应只分配小型索引")
}

func TestCompactionRawHistoryMatchesDecodedContract(t *testing.T) {
	fixtures := []string{
		` {"input":[{"type":"compaction_trigger"},{"type":"message","content":"keep","sequence":9007199254740993}],"tail":true} `,
		`{"input":[{"type":"compaction_trigger"},null,42,"text",{"type":"compaction_trigger"}]}`,
		`{"input":[{"type":"compaction\u005ftrigger"},{"type":"message","content":"compaction_trigger"}]}`,
		`{"input":[{"type":"compaction_trigger","extra":"first"},{"type":"message"},{"type":"compaction_trigger","extra":"last"}]}`,
		`{"input":[{"type":"message","type":"compaction_trigger"},{"type":"message"}]}`,
		`{"input":[{"type":"compaction_trigger"}],"input":[{"type":"message"}]}`,
		`{"input":[{"type":"message"}],"input":[{"type":"compaction_trigger"},{"type":"message"}]}`,
		`{"input":[{"type":"message"},{"type":"compaction_trigger","extra":"keep"}]}`,
		`{"input":"compaction_trigger"}`,
		`{"input":[{"type":"compaction_trigger"}]} trailing`,
		`[{"type":"compaction_trigger"}]`,
		`null`,
	}
	for _, fixture := range fixtures {
		t.Run(fixture, func(t *testing.T) {
			body := []byte(fixture)
			original := bytes.Clone(body)
			want, wantChanged, wantErr := normalizeCompactionTriggerInputOrderDecoded(body)
			got, changed, err := NormalizeCompactionTriggerInputOrder(body)
			require.Equal(t, wantErr != nil, err != nil)
			require.Equal(t, wantChanged, changed)
			require.Equal(t, original, body)
			if err == nil {
				if changed {
					require.JSONEq(t, string(want), string(got))
				} else {
					require.Equal(t, want, got)
				}
			}
		})
	}
}
