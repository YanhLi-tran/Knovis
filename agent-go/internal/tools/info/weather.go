package info

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"agent-go/internal/tools"
)

const (
	geocodingURL = "https://geocoding-api.open-meteo.com/v1/search"
	weatherURL   = "https://api.open-meteo.com/v1/forecast"
)

var weatherCodeMap = map[int]string{
	0: "晴天", 1: "大致晴朗", 2: "局部多云", 3: "阴天",
	45: "雾", 48: "冻雾",
	51: "小毛毛雨", 53: "中毛毛雨", 55: "大毛毛雨",
	61: "小雨", 63: "中雨", 65: "大雨",
	71: "小雪", 73: "中雪", 75: "大雪",
	80: "小阵雨", 81: "中阵雨", 82: "大阵雨",
	95: "雷暴", 96: "雷暴伴小冰雹", 99: "雷暴伴大冰雹",
}

type geocodingResponse struct {
	Results []struct {
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
		Name      string  `json:"name"`
		Country   string  `json:"country"`
		Admin1    string  `json:"admin1"`
		Population int    `json:"population"`
		FeatureCode string `json:"feature_code"`
	} `json:"results"`
}

type weatherResponse struct {
	Current struct {
		Temperature2m        float64 `json:"temperature_2m"`
		RelativeHumidity2m   float64 `json:"relative_humidity_2m"`
		WindSpeed10m         float64 `json:"wind_speed_10m"`
		WeatherCode          int     `json:"weather_code"`
	} `json:"current"`
	Daily struct {
		Time             []string  `json:"time"`
		Temperature2mMax []float64 `json:"temperature_2m_max"`
		Temperature2mMin []float64 `json:"temperature_2m_min"`
		PrecipitationSum []float64 `json:"precipitation_sum"`
		WeatherCode      []int     `json:"weather_code"`
	} `json:"daily"`
}

// httpGet JSON GET 请求
func httpGet(target string, params url.Values, result any) error {
	if params != nil {
		target = target + "?" + params.Encode()
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(target)
	if err != nil {
		log.Printf("[ERROR][tools] HTTP 调用失败 url=%s err=%v", target, err)
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, result)
}

// geocodeCity 城市名 → 经纬度
func geocodeCity(ctx context.Context, args map[string]any) (string, error) {
	city, _ := args["city"].(string)
	if city == "" {
		return "", fmt.Errorf("缺少参数 city")
	}

	params := url.Values{
		"name":     {city},
		"count":    {"10"},
		"language": {"zh"},
		"format":   {"json"},
	}

	var geo geocodingResponse
	if err := httpGet(geocodingURL, params, &geo); err != nil {
		log.Printf("[WARN][tools] 地理编码失败(降级) city=%s err=%v", city, err)
		return "", fmt.Errorf("地理编码失败: %w", err)
	}

	if len(geo.Results) == 0 {
		return fmt.Sprintf(`{"error":"未找到城市 '%s'"}`, city), nil
	}

	// 选人口最多/行政级别最高的结果
	best := geo.Results[0]
	for _, r := range geo.Results[1:] {
		if r.Population > best.Population {
			best = r
		}
	}

	result := map[string]any{
		"lat":     best.Latitude,
		"lon":     best.Longitude,
		"name":    best.Name,
		"country": best.Country,
		"admin1":  best.Admin1,
	}
	data, _ := json.Marshal(result)
	return string(data), nil
}

// getWeatherForecast 经纬度 → 天气预报
func getWeatherForecast(ctx context.Context, args map[string]any) (string, error) {
	lat, _ := args["lat"].(float64)
	lon, _ := args["lon"].(float64)
	if lat == 0 && lon == 0 {
		// 尝试从 string 转换
		if latStr, ok := args["lat"].(string); ok {
			lat, _ = strconv.ParseFloat(latStr, 64)
		}
		if lonStr, ok := args["lon"].(string); ok {
			lon, _ = strconv.ParseFloat(lonStr, 64)
		}
	}
	if lat == 0 && lon == 0 {
		return "", fmt.Errorf("缺少参数 lat/lon")
	}

	params := url.Values{
		"latitude":  {fmt.Sprintf("%g", lat)},
		"longitude": {fmt.Sprintf("%g", lon)},
		"current":   {"temperature_2m,relative_humidity_2m,wind_speed_10m,weather_code"},
		"daily":     {"temperature_2m_max,temperature_2m_min,precipitation_sum,weather_code"},
		"timezone":  {"Asia/Shanghai"},
		"forecast_days": {"3"},
	}

	var w weatherResponse
	if err := httpGet(weatherURL, params, &w); err != nil {
		log.Printf("[ERROR][tools] 天气数据获取失败 lat=%g lon=%g err=%v", lat, lon, err)
		return "", fmt.Errorf("天气数据获取失败: %w", err)
	}

	desc := weatherCodeMap[w.Current.WeatherCode]
	lines := []string{
		"【天气报告】",
		"",
		fmt.Sprintf("当前天气：%s", desc),
		fmt.Sprintf("  温度：%.1f°C", w.Current.Temperature2m),
		fmt.Sprintf("  相对湿度：%.0f%%", w.Current.RelativeHumidity2m),
		fmt.Sprintf("  风速：%.1f km/h", w.Current.WindSpeed10m),
		"",
		"未来3天预报：",
	}
	for i := 0; i < len(w.Daily.Time) && i < 3; i++ {
		dayDesc := weatherCodeMap[w.Daily.WeatherCode[i]]
		lines = append(lines, fmt.Sprintf("  %s：%s，%.1f°C / %.1f°C，降水 %.1f mm",
			w.Daily.Time[i], dayDesc,
			w.Daily.Temperature2mMax[i], w.Daily.Temperature2mMin[i],
			w.Daily.PrecipitationSum[i]))
	}

	return joinLines(lines), nil
}

// getWeather 一步到位查天气
func getWeather(ctx context.Context, args map[string]any) (string, error) {
	city, _ := args["city"].(string)
	if city == "" {
		return "", fmt.Errorf("缺少参数 city")
	}

	geoResult, err := geocodeCity(ctx, args)
	if err != nil {
		return "", err
	}

	var geo map[string]any
	if err := json.Unmarshal([]byte(geoResult), &geo); err != nil {
		log.Printf("[ERROR][tools] 天气结果 JSON 解析失败 city=%s err=%v", city, err)
		return "", err
	}
	if errMsg, ok := geo["error"]; ok {
		return fmt.Sprintf(`{"error":"%s"}`, errMsg), nil
	}

	lat, _ := geo["lat"].(float64)
	lon, _ := geo["lon"].(float64)
	name, _ := geo["name"].(string)
	country, _ := geo["country"].(string)
	admin1, _ := geo["admin1"].(string)

	forecast, err := getWeatherForecast(ctx, map[string]any{"lat": lat, "lon": lon})
	if err != nil {
		return "", err
	}

	locationStr := fmt.Sprintf("%s %s %s", country, admin1, name)
	result := fmt.Sprintf("【%s】\n%s", locationStr, forecast)
	log.Printf("[INFO][tools] 天气查询成功 city=%s result_len=%d", city, len(result))
	return result, nil
}

// RegisterWeatherTools 注册天气相关工具
func RegisterWeatherTools(registry *tools.Registry) {
	registry.Register(&tools.Tool{
		Name:        "geocode_city",
		Description: "将城市名称转换为经纬度坐标，用于后续天气查询等需要坐标的操作",
		Category:    "info",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city": map[string]any{
					"type":        "string",
					"description": "城市名称，支持中文，例如 '北京'、'上海'、'深圳'",
				},
			},
			"required": []string{"city"},
		},
		Handler: geocodeCity,
	})

	registry.Register(&tools.Tool{
		Name:        "get_weather_forecast",
		Description: "根据经纬度查询当前天气及未来3天预报，包含温度、湿度、风速、天气状况",
		Category:    "info",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"lat": map[string]any{
					"type":        "number",
					"description": "纬度，例如 31.23（上海）",
				},
				"lon": map[string]any{
					"type":        "number",
					"description": "经度，例如 121.47（上海）",
				},
			},
			"required": []string{"lat", "lon"},
		},
		Handler: getWeatherForecast,
	})

	registry.Register(&tools.Tool{
		Name:        "get_weather",
		Description: "查询指定城市的当前天气及未来3天预报（一步到位，自动完成地理编码+天气查询）",
		Category:    "info",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city": map[string]any{
					"type":        "string",
					"description": "城市名称，支持中文，例如 '北京'、'上海'、'深圳'、'杭州'",
				},
			},
			"required": []string{"city"},
		},
		Handler: getWeather,
	})
}
