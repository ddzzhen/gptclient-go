package sentinel

import "testing"

func TestParseStreamHandoffSubscribeTopic(t *testing.T) {
	evt := map[string]interface{}{
		"type": "stream_handoff",
		"options": []interface{}{
			map[string]interface{}{"type": "subscribe_ws_topic", "topic_id": "conversation-turn-abc"},
		},
	}
	ok, topic := parseStreamHandoff(evt)
	if !ok || topic != "conversation-turn-abc" {
		t.Fatalf("parseStreamHandoff=%v,%q", ok, topic)
	}
}

func TestCheckImageTaskIDVariants(t *testing.T) {
	result := &ChatResult{}
	checkImageTaskID(map[string]interface{}{
		"v": map[string]interface{}{
			"message": map[string]interface{}{
				"metadata": map[string]interface{}{"image_gen_task_id": "task-1"},
			},
		},
	}, result)
	if result.ImageTaskID != "task-1" {
		t.Fatalf("ImageTaskID=%q", result.ImageTaskID)
	}

	result = &ChatResult{}
	checkImageTaskID(map[string]interface{}{
		"v": map[string]interface{}{
			"message": map[string]interface{}{
				"metadata": map[string]interface{}{"ghostrider": map[string]interface{}{}},
			},
		},
	}, result)
	if result.ImageTaskID != "ghostrider" {
		t.Fatalf("ImageTaskID ghostrider=%q", result.ImageTaskID)
	}
}
