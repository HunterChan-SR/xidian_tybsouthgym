package main

import (
	xidianTybsouthgymClient "xidian_tybsouthgym/client"
)

const Domain = "tybsouthgym.xidian.edu.cn"
const HostUrl = "https://" + Domain + "/"

func main() {
	// JWTUserToken := "eyJ0eXAiOiJKV1QiLCJhbGciOiJIUzI1NiJ9.eyJuYW1lIjoiZjZkNGY4YmQtN2Y1NS00Y2RkLWE4MjktMDMxZjE5YmE5OTVhIiwiZXhwIjoxNzMxMDcyMTM4LjAsImp0aSI6ImxnIiwiaWF0IjoiMjAyNC0xMS0wMSAxMzoyMjoxNyJ9.QkoU7AVZSxq_B7wuurRazjfnqdkU0YbJUCjJWD9A2cg"
	// UserId := "f6d4f8bd-7f55-4cdd-a829-031f19ba995a"
	// WXOpenId := "24151213727"
	// jar, err := cookiejar.New(nil)
	// if err != nil {
	// 	panic(err)
	// }
	// client := &http.Client{
	// 	Jar: jar,
	// }
	// client.Jar.SetCookies(
	// 	&url.URL{Host: Domain},
	// 	[]*http.Cookie{
	// 		{Name: "JWTUserToken", Value: JWTUserToken},
	// 		{Name: "UserId", Value: UserId},
	// 		{Name: "WXOpenId", Value: WXOpenId},
	// 	},
	// )
	// resp, _ := client.Get("https://tybsouthgym.xidian.edu.cn/Field/GetVenueStateNew?dateadd=0&TimePeriod=2&VenueNo=01&FieldTypeNo=002&_=1730441416565")
	// defer resp.Body.Close()
	// bodyBytes, _ := io.ReadAll(resp.Body)
	// fmt.Println(string(bodyBytes))
	//把bodyBytes保存至文件
	// file, _ := os.Create("./userinfo.html")
	// defer file.Close()
	// file.Write(bodyBytes)

	xdgym := xidianTybsouthgymClient.New()

	xdgym.GetOrderByTime()
}
