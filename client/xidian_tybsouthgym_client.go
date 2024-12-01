package xidianTybsouthgymClient

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"xidian_tybsouthgym/client/models"

	"github.com/gorilla/websocket"
)

const Domain = "tybsouthgym.xidian.edu.cn"
const HostUrl = "https://" + Domain

// type UserCookies struct {
// 	Domain                                      string
// 	_xsrf                                       string
// 	tk_7ec1e7a85f61aecc31786cc9ab119c28e8d96533 string
// 	JWTUserToken                                string
// 	UserId                                      string
// 	WXOpenId                                    string
// }

type XidianTybsouthgymClient struct {
	client       *http.Client
	successCount int
	demand       int
}

func LoadCookie() (JWTUserToken, UserId, WXOpenId string, err error) {
	file, err := os.OpenFile("cookie.txt", os.O_RDWR|os.O_CREATE, 0666)
	if err != nil {
		return "", "", "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lines := make([]string, 0)

	for scanner.Scan() {
		lines = append(lines, strings.TrimSpace(scanner.Text()))
	}

	if err := scanner.Err(); err != nil {
		return "", "", "", err
	}

	if len(lines) < 3 {
		return "", "", "", fmt.Errorf("文件内容不足，缺少必要信息")
	}

	JWTUserToken = lines[0]
	UserId = lines[1]
	WXOpenId = lines[2]

	return JWTUserToken, UserId, WXOpenId, nil
}
func SaveCookie(JWTUserToken, UserId, WXOpenId string) error {
	file, err := os.OpenFile("cookie.txt", os.O_WRONLY|os.O_TRUNC|os.O_CREATE, 0666)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	_, err = writer.WriteString(fmt.Sprintf("%s\n%s\n%s\n", JWTUserToken, UserId, WXOpenId))
	if err != nil {
		return err
	}

	err = writer.Flush()
	if err != nil {
		return err
	}

	return nil
}

// new
func New(JWTUserToken, UserId, WXOpenId string, demand int) *XidianTybsouthgymClient {
	// jwtUserToken := flag.String("token", JWTUserToken, "JWTUserToken")
	// userId := flag.String("id", UserId, "UserId")
	// wxOpenId := flag.String("wx", WXOpenId, "WXOpenId")

	jar, err := cookiejar.New(nil)
	if err != nil {
		panic(err)
	}
	jar_url, err := url.Parse(HostUrl)
	if err != nil {
		panic(err)
	}
	jar.SetCookies(
		jar_url,
		[]*http.Cookie{
			{Name: "JWTUserToken", Value: JWTUserToken},
			{Name: "UserId", Value: UserId},
			{Name: "WXOpenId", Value: WXOpenId},
		},
	)
	client := &http.Client{
		Jar: jar,
	}
	return &XidianTybsouthgymClient{successCount: 0, client: client, demand: demand}
}

func DefaultClient() *XidianTybsouthgymClient {
	// cookie
	JWTUserToken, UserId, WXOpenId, err := LoadCookie()
	if err != nil || JWTUserToken == "" || UserId == "" || WXOpenId == "" {
		fmt.Println("cookie文件不存在,请输入cookie:")
		fmt.Println("JWTUserToken: ")
		fmt.Scan(&JWTUserToken)
		fmt.Println("UserId: ")
		fmt.Scan(&UserId)
		fmt.Println("WXOpenId: ")
		fmt.Scan(&WXOpenId)
	}
	SaveCookie(JWTUserToken, UserId, WXOpenId)
	jwtUserToken := flag.String("token", JWTUserToken, "JWTUserToken")
	userId := flag.String("id", UserId, "UserId")
	wxOpenId := flag.String("wx", WXOpenId, "WXOpenId")

	jar, err := cookiejar.New(nil)
	if err != nil {
		panic(err)
	}
	jar_url, err := url.Parse(HostUrl)
	if err != nil {
		panic(err)
	}
	fmt.Println(jar_url, "cookiejar:")
	jar.SetCookies(
		jar_url,
		[]*http.Cookie{
			{Name: "JWTUserToken", Value: *jwtUserToken},
			{Name: "UserId", Value: *userId},
			{Name: "WXOpenId", Value: *wxOpenId},
		},
	)

	for _, cookie := range jar.Cookies(jar_url) {
		fmt.Println(cookie.Name, cookie.Value)
	}

	client := &http.Client{
		Jar: jar,
	}
	fmt.Println("需要订单数量:")
	demand := 0
	if _, err := fmt.Scan(&demand); err != nil || demand <= 0 {
		panic(fmt.Errorf("输入错误"))
	}
	ans := &XidianTybsouthgymClient{successCount: 0, client: client, demand: demand}
	if ans.CheckUserStatus() {
		return ans
	} else {
		fmt.Println("用户cookie失效,请删除cookie.txt文件后重试")
		panic(fmt.Errorf("用户未登录"))
	}
}

type NoMethodError struct {
}

func (e NoMethodError) Error() string {
	return "错误请求方法"
}

func (c *XidianTybsouthgymClient) Request(method, path string, params string, body url.Values) (*http.Response, error) {
	if method == "GET" {
		//fmt.Println(method, HostUrl+path+"?"+params)
		if params == "" {
			return c.client.Get(HostUrl + path)
		} else {
			return c.client.Get(HostUrl + path + "?" + params)
		}
	} else if method == "POST" {
		return c.client.PostForm(HostUrl+path, body)
	} else {
		return nil, NoMethodError{}
	}
}

type DateTimeError struct{}

func (e DateTimeError) Error() string {
	return "时间格式错误"
}

func (c *XidianTybsouthgymClient) PostOrder(FieldNo, FieldTypeNo, FieldName, BeginTime, Endtime, Price, dateadd, VenueNo string) []byte {

	param := url.Values{}
	checkdata := fmt.Sprintf("[{\"FieldNo\":\"%s\",\"FieldTypeNo\":\"%s\",\"FieldName\":\"%s\",\"BeginTime\":\"%s\",\"Endtime\":\"%s\",\"Price\":\"%s\"}]",
		FieldNo,
		FieldTypeNo,
		FieldName,
		BeginTime,
		Endtime,
		Price,
	)
	param.Set("checkdata", checkdata)
	param.Set("dateadd", dateadd)
	param.Set("VenueNo", VenueNo)
	resp, _ := c.Request("GET", "/Field/OrderField", param.Encode(), nil)

	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	return body
}

type rsp struct {
	Message string `json:"message"`
	Type    int    `json:"type"`
}

func (c *XidianTybsouthgymClient) CheckUserStatus() bool {
	resp, err := c.Request("GET", "/User/CheckUserStatus", "", nil)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)
	return string(bodyBytes) == "1"
}

func (c *XidianTybsouthgymClient) GetOrderByTime() {
	fieldType := 0
	fmt.Println("fieldType(1羽毛球、2乒乓球、3篮球):")
	if _, err := fmt.Scan(&fieldType); err != nil || fieldType < 1 || fieldType > 3 {
		panic(fmt.Errorf("输入错误"))
	}
	dateAdd := ""
	fmt.Println("dateAdd(0表示今天、1表示明天、2表示后天等等):")
	if _, err := fmt.Scan(&dateAdd); err != nil {
		panic(err)
	}
	if x, err := strconv.Atoi(dateAdd); err != nil || x < 0 {
		panic(DateTimeError{})
	}
	TimePeriod := ""
	fmt.Println("TimePeriod(0表示上午、1表示下午、2表示晚上):")
	if _, err := fmt.Scan(&TimePeriod); err != nil {
		panic(err)
	}
	if TimePeriod != "0" && TimePeriod != "1" && TimePeriod != "2" {
		panic(DateTimeError{})
	}
	FieldTypeNo := ""
	if fieldType == 1 {
		FieldTypeNo = models.YMQ{}.GetFieldTypeNo()
	} else if fieldType == 2 {
		FieldTypeNo = models.PPQ{}.GetFieldTypeNo()
	} else if fieldType == 3 {
		FieldTypeNo = models.LQ{}.GetFieldTypeNo()
	} else {
		panic(errors.New("参数错误"))
	}

	for {
		params := "dateadd=" + dateAdd + "&TimePeriod=" + TimePeriod + "&VenueNo=01" + "&FieldTypeNo=" + FieldTypeNo

		resp, err := c.Request("GET", "/Field/GetVenueStateNew", params, nil)
		if err != nil {
			panic(err)
		}

		orders, err := JsonToList(resp.Body)
		if err != nil {
			fmt.Println(err)
			panic(err)
		}
		fmt.Println("已被预订:")
		for i, order := range orders {
			if order.FieldState == "0" {
				continue
			}
			fmt.Println("id:", i,
				"BeginTime:", order.BeginTime,
				"EndTime:", order.EndTime,
				"Count", order.Count,
				"FieldNo", order.FieldNo,
				"FieldName", order.FieldName,
				"FieldTypeNo", order.FieldTypeNo,
				"FinalPrice", order.FinalPrice,
				"TimeStatus", order.TimeStatus,
				"FieldState", order.FieldState,
				"IsHalfHour", order.IsHalfHour,
				"ShowWidth", order.ShowWidth,
				"DateBeginTime", order.DateBeginTime,
				"DateEndTime", order.DateEndTime,
				"TimePeriod", order.TimePeriod,
				"MembeName", order.MembeName)
		}
		for _, order := range orders {
			if order.FieldState == "0" {
				res := c.PostOrder(order.FieldNo, order.FieldTypeNo, order.FieldName, order.BeginTime, order.EndTime, order.FinalPrice, dateAdd, "01")
				fmt.Println("下单", order.FieldName, "中")
				data := rsp{}
				json.Unmarshal(res, &data)
				if data.Message != "" && data.Type == 3 {
					fmt.Println("下单失败", data.Message)
				} else {
					fmt.Println(order.FieldName, "号场地预定成功，请尽快支付！")
					c.successCount++
					if c.successCount >= c.demand {
						panic(fmt.Errorf("订单数量已达需求"))
					}
				}
				time.Sleep(10 * time.Second)
			}
		}
		time.Sleep(10 * time.Second)

	}
}

func (c *XidianTybsouthgymClient) GetOrderByTime2(fieldType, dateAdd, TimePeriod int, conn *websocket.Conn) bool {
	FieldTypeNo := ""
	if fieldType == 1 {
		FieldTypeNo = models.YMQ{}.GetFieldTypeNo()
	} else if fieldType == 2 {
		FieldTypeNo = models.PPQ{}.GetFieldTypeNo()
	} else if fieldType == 3 {
		FieldTypeNo = models.LQ{}.GetFieldTypeNo()
	} else {
		conn.WriteMessage(websocket.TextMessage, []byte("参数错误"))
		return false
	}
	if dateAdd == 2 {
		for {
			_, p, err := conn.ReadMessage()
			if err != nil {
				conn.WriteMessage(websocket.TextMessage, []byte(err.Error()))
				return false
			}
			sp := string(p)
			if sp == "stop" {
				conn.WriteMessage(websocket.TextMessage, []byte("已停止"))
				return false
			} else if sp == "continue" {
				conn.WriteMessage(websocket.TextMessage, []byte("continue,未到12:00"))
			}

			currentTime := time.Now()
			if currentTime.Hour() >= 12 {
				break
			}
			// 休眠 1 秒
			time.Sleep(1 * time.Second)
		}
	}

	for {
		_, p, err := conn.ReadMessage()
		if err != nil {
			conn.WriteMessage(websocket.TextMessage, []byte(err.Error()))
			return false
		}
		sp := string(p)
		if sp == "stop" {
			conn.WriteMessage(websocket.TextMessage, []byte("已停止"))
			return false
		} else if sp == "continue" {
			conn.WriteMessage(websocket.TextMessage, []byte("continue"))
		}

		params := "dateadd=" + strconv.Itoa(dateAdd) + "&TimePeriod=" + strconv.Itoa(TimePeriod) + "&VenueNo=01" + "&FieldTypeNo=" + FieldTypeNo

		resp, err := c.Request("GET", "/Field/GetVenueStateNew", params, nil)
		if err != nil {
			conn.WriteMessage(websocket.TextMessage, []byte(err.Error()))
			return false
		}
		orders, err := JsonToList(resp.Body)
		if err != nil {
			conn.WriteMessage(websocket.TextMessage, []byte(err.Error()))
			return false
		}
		// conn.WriteMessage(websocket.TextMessage, []byte("已被预订:"))
		// for i, order := range orders {
		// 	if order.FieldState == "0" {
		// 		continue
		// 	}
		// 	output := fmt.Sprint("id:", i,
		// 		"BeginTime:", order.BeginTime,
		// 		"EndTime:", order.EndTime,
		// 		"Count", order.Count,
		// 		"FieldNo", order.FieldNo,
		// 		"FieldName", order.FieldName,
		// 		"FieldTypeNo", order.FieldTypeNo,
		// 		"FinalPrice", order.FinalPrice,
		// 		"TimeStatus", order.TimeStatus,
		// 		"FieldState", order.FieldState,
		// 		"IsHalfHour", order.IsHalfHour,
		// 		"ShowWidth", order.ShowWidth,
		// 		"DateBeginTime", order.DateBeginTime,
		// 		"DateEndTime", order.DateEndTime,
		// 		"TimePeriod", order.TimePeriod,
		// 		"MembeName", order.MembeName)
		// 	conn.WriteMessage(websocket.TextMessage, []byte(output))
		// }
		slices.Reverse(orders)
		for _, order := range orders {
			// if order.FieldState == "0" {
			// 	res := c.PostOrder(order.FieldNo, order.FieldTypeNo, order.FieldName, order.BeginTime, order.EndTime, order.FinalPrice, strconv.Itoa(dateAdd), "01")
			// 	output := fmt.Sprint("下单", order.FieldName, "中")
			// 	conn.WriteMessage(websocket.TextMessage, []byte(output))
			// 	data := rsp{}
			// 	json.Unmarshal(res, &data)
			// 	if data.Message != "" && data.Type == 3 {
			// 		output = fmt.Sprint("下单失败", data.Message)
			// 		conn.WriteMessage(websocket.TextMessage, []byte(output))
			// 	} else {
			// 		output = fmt.Sprint(order.FieldName, "号场地预定成功，请尽快支付！")
			// 		conn.WriteMessage(websocket.TextMessage, []byte(output))
			// 		c.successCount++
			// 		if c.successCount >= c.demand {
			// 			conn.WriteMessage(websocket.TextMessage, []byte("已达到预定数量，程序退出"))
			// 			return true
			// 		}
			// 	}
			// 	time.Sleep(10 * time.Second)
			// }
			if order.FieldState == "0" {
				res := c.PostOrder(order.FieldNo, order.FieldTypeNo, order.FieldName, order.BeginTime, order.EndTime, order.FinalPrice, strconv.Itoa(dateAdd), "01")
				output := fmt.Sprint("下单", order.FieldName, "中")
				conn.WriteMessage(websocket.TextMessage, []byte(output))
				data := rsp{}
				json.Unmarshal(res, &data)
				if data.Message != "" && data.Type == 3 {
					output = fmt.Sprint("下单失败", data.Message)
					conn.WriteMessage(websocket.TextMessage, []byte(output))
				} else {
					output = fmt.Sprint("success", order.FieldName, "号场地预定成功，请尽快支付！")
					conn.WriteMessage(websocket.TextMessage, []byte(output))
					c.successCount++
					if c.successCount >= c.demand {
						conn.WriteMessage(websocket.TextMessage, []byte("已达到预定数量，程序退出"))
						return true
					}
				}
				break
			}
		}
		time.Sleep(6 * time.Second)
	}
}

// {"IsCardPay":null,"MemberNo":null,"Discount":null,"ConType":null,"type":1,"errorcode":0,"message":"获取成功","resultdata":"[{\"BeginTime\":\"18:00\",\"EndTime\":\"19:00\",\"Count\":\"14\",\"FieldNo\":\"YMQ001\",\"FieldName\":\"羽毛球1\",\"FieldTypeNo\":\"001\",\"FinalPrice\":\"12.00\",\"TimeStatus\":\"1\",\"FieldState\":\"1\",\"IsHalfHour\":\"0\",\"ShowWidth\":\"100\",\"DateBeginTime\":\"2024-11-01 18:00:00\",\"DateEndTime\":\"2024-11-01 19:00:00\",\"TimePeriod\":\"2\",\"MembeName\":\"刘**已预订\"},{\"BeginTime\":\"18:00\",\"EndTime\":\"19:00\",\"Count\":\"14\",\"FieldNo\":\"YMQ002\",\"FieldName\":\"羽毛球2\",\"FieldTypeNo\":\"001\",\"FinalPrice\":\"12.00\",\"TimeStatus\":\"1\",\"FieldState\":\"1\",\"IsHalfHour\":\"0\",\"ShowWidth\":\"100\",\"DateBeginTime\":\"2024-11-01 18:00:00\",\"DateEndTime\":\"2024-11-01 19:00:00\",\"TimePeriod\":\"2\",\"MembeName\":\"曹**已预订\"}}
func JsonToList(body io.ReadCloser) ([]models.Order, error) {

	// 已知body为访问返回的json
	// 先解析message，如果message不为"获取成功"则报错，否则按照模板解析json的resultdata
	defer body.Close()

	var response models.Response
	if err := json.NewDecoder(body).Decode(&response); err != nil {
		return nil, err
	}

	if response.Message != "获取成功" {
		return nil, errors.New("请求失败: " + response.Message)
	}

	var orders []models.Order
	if err := json.Unmarshal([]byte(response.ResultData), &orders); err != nil {
		return nil, err
	}

	return orders, nil
}
