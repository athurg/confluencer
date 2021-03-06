package confluence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 获取指定ID的内容
func (cli *Client) ContentById(id string) (Content, error) {
	return cli.ContentByIdWithOpt(id, nil)
}

// 获取指定ID的内容（可以设置获取选项）
func (cli *Client) ContentByIdWithOpt(id string, opt url.Values) (Content, error) {
	if opt == nil {
		opt = url.Values{}
	}

	// 缺省展开version便于后期更新时递增版本号
	if opt.Get("expand") == "" {
		opt.Set("expand", "version")
	}

	resp, err := cli.ApiGET("/content/"+id, opt)
	if err != nil {
		return Content{}, fmt.Errorf("执行请求失败: %s", err)
	}

	defer resp.Body.Close()

	var info struct {
		ErrorResp
		Content
		Results []Content
	}

	err = json.NewDecoder(resp.Body).Decode(&info)
	if err != nil {
		return Content{}, fmt.Errorf("解析响应失败: %s", err)
	}

	if info.StatusCode != 0 {
		return Content{}, fmt.Errorf("[%d]%s", info.StatusCode, info.Message)
	}

	return info.Content, nil
}

//获取指定空间、标题的内容
func (cli *Client) ContentBySpaceAndTitle(space, title string) (Content, error) {
	q := url.Values{
		"title":    {title},
		"spaceKey": {space},
		"expand":   {"version,body.storage,ancestors"},
	}

	resp, err := cli.ApiGET("/content", q)
	if err != nil {
		return Content{}, fmt.Errorf("执行请求失败: %s", err)
	}

	defer resp.Body.Close()

	var info struct {
		ErrorResp
		PageResp
		Results []Content
	}

	err = json.NewDecoder(resp.Body).Decode(&info)
	if err != nil {
		return Content{}, fmt.Errorf("解析响应失败: %s", err)
	}

	if resp.StatusCode != http.StatusOK {
		return Content{}, fmt.Errorf("[%d]%s", info.StatusCode, info.Message)
	}

	switch info.Size {
	case 0:
		return Content{}, nil
	case 1:
		return info.Results[0], nil
	default:
		return Content{}, fmt.Errorf("找到%d条记录", info.Size)
	}
}

//在指定空间创建页面
func (cli *Client) PageCreateInSpace(space, parentId, title, data string) (Content, error) {
	return cli.ContentCreateInSpace("page", space, parentId, title, data)
}

//在指定空间创建内容
func (cli *Client) ContentCreateInSpace(contentType, space, parentId, title, data string) (Content, error) {
	content := Content{Type: contentType, Title: title}
	content.Space.Key = space
	content.SetStorageBody(data)

	//FIXME: 这里指定了创建信息，但是好像没什么用
	content.Version.Message = time.Now().Local().Format("机器人创建于2006-01-02 15:04:05")

	//设置父页面
	if parentId != "" {
		content.Ancestors = []Content{
			{
				Id: parentId,
				Space: Space{
					Key: content.Space.Key,
				},
			},
		}
	}

	resp, err := cli.ApiPOST("/content", content)
	if err != nil {
		return Content{}, fmt.Errorf("执行请求失败: %s", err)
	}

	defer resp.Body.Close()

	var info struct {
		ErrorResp
		Content
	}
	err = json.NewDecoder(resp.Body).Decode(&info)
	if err != nil {
		return Content{}, fmt.Errorf("解析响应失败: %s", err)
	}

	if resp.StatusCode != http.StatusOK {
		return Content{}, fmt.Errorf("[%d]%s", resp.StatusCode, info.Message)
	}

	return info.Content, nil
}

//更新指定的内容
func (cli *Client) ContentUpdate(content Content) (Content, error) {
	resp, err := cli.ApiPUT("/content/"+content.Id, content)
	if err != nil {
		return Content{}, fmt.Errorf("执行请求失败: %s", err)
	}

	defer resp.Body.Close()

	var info struct {
		ErrorResp
		Content
	}
	err = json.NewDecoder(resp.Body).Decode(&info)
	if err != nil {
		return Content{}, fmt.Errorf("解析响应失败: %s", err)
	}

	if info.StatusCode != 0 {
		return Content{}, fmt.Errorf("[%d]%s", info.StatusCode, info.Message)
	}

	return info.Content, nil
}

//从指定空间查找或创建指定标题的内容
func (cli *Client) DrawFile(space, parentId, title, wikiDirPrefix, data string) (Content, error) {
	//内容中的空行会被Confluence保存时自动去掉
	//因此前先去掉，以避免对比内容变化时受到影响
	data = strings.TrimSuffix(strings.TrimPrefix(data, "\n"), "\n")

	//获取当前页面的内容
	content, err := cli.ContentBySpaceAndTitle(space, title)
	if err != nil {
		return Content{}, fmt.Errorf("查找%s出错: %s", title, err)
	}

	// 不存在则创建
	if content.Id == "" {
		return cli.PageCreateInSpace(space, parentId, title, data)
	}

	// 获取文件的路径
	pagePath := ""
	lastAncestorId := ""
	for _, ancestor := range content.Ancestors {
		pagePath += "/" + ancestor.Title
		lastAncestorId = ancestor.Id
	}
	pagePath += "/" + content.Title

	//存在，但不在指定的路径下，报错结束
	if !strings.HasPrefix(pagePath, wikiDirPrefix) {
		return Content{}, fmt.Errorf("一个标题为 '%v' 的页面已经存在于该空间中。为您的页面输入一个不同的标题。", title)
	} else {
		//存在：对比内容是否有变化
		newValue, err := cli.ContentBodyConvertTo(data, "storage", "view")
		if err != nil {
			return Content{}, fmt.Errorf("转换新内容失败: %s", err)
		}

		// 去除原文件的备注宏
		if content.Body.Storage.Value != "" {
			content.Body.Storage.Value = strings.Split(content.Body.Storage.Value, ConfluenceNoteSplite)[0]
		}
		oldValue, err := cli.ContentBodyConvertTo(content.Body.Storage.Value, "storage", "view")
		if err != nil {
			return Content{}, fmt.Errorf("转换旧内容失败: %s", err)
		}

		if newValue == oldValue && lastAncestorId == parentId {
			return content, nil
		}

		// 存在则否则更新
		content.Space.Key = space
		content.Version.Number += 1
		content.Version.Message = time.Now().Local().Format("机器人更新于2006-01-02 15:04:05")
		content.SetStorageBody(data)

		//设置父页面
		if parentId != "" {
			content.Ancestors = []Content{
				{
					Id: parentId,
					Space: Space{
						Key: content.Space.Key,
					},
				},
			}
		}

		return cli.ContentUpdate(content)
	}
}

func (cli *Client) DrawFileWithNoteMacro(space, parentId, title, wikiDirPrefix, data, extraInfo string) (Content, error) {
	//内容中的空行会被Confluence保存时自动去掉
	//因此前先去掉，以避免对比内容变化时受到影响
	data = strings.TrimSuffix(strings.TrimPrefix(data, "\n"), "\n")

	//获取当前页面的内容
	content, err := cli.ContentBySpaceAndTitle(space, title)
	if err != nil {
		return Content{}, fmt.Errorf("查找%s出错: %s", title, err)
	}

	// 不存在则创建
	if content.Id == "" {
		data += fmt.Sprintf(ConfluenceNoteMacro, extraInfo)
		return cli.PageCreateInSpace(space, parentId, title, data)
	}

	// 获取文件的路径
	pagePath := ""
	lastAncestorId := ""
	for _, ancestor := range content.Ancestors {
		pagePath += "/" + ancestor.Title
		lastAncestorId = ancestor.Id
	}
	pagePath += "/" + content.Title

	//存在，但不在指定的路径下，报错结束
	if !strings.HasPrefix(pagePath, wikiDirPrefix) {
		return Content{}, fmt.Errorf("一个标题为 '%v' 的页面已经存在于该空间中。为您的页面输入一个不同的标题。", title)
	} else {
		//存在：对比内容是否有变化
		newValue, err := cli.ContentBodyConvertTo(data, "storage", "view")
		if err != nil {
			return Content{}, fmt.Errorf("转换新内容失败: %s", err)
		}

		// 去除原文件的备注宏
		if content.Body.Storage.Value != "" {
			content.Body.Storage.Value = strings.Split(content.Body.Storage.Value, ConfluenceNoteSplite)[0]
		}
		oldValue, err := cli.ContentBodyConvertTo(content.Body.Storage.Value, "storage", "view")
		if err != nil {
			return Content{}, fmt.Errorf("转换旧内容失败: %s", err)
		}

		if newValue == oldValue && lastAncestorId == parentId {
			return content, nil
		} else {
			// 如果确定要更新confluence页面，那么这里添加一个备注宏
			data += fmt.Sprintf(ConfluenceNoteMacro, extraInfo)
		}

		// 存在则否则更新
		content.Space.Key = space
		content.Version.Number += 1
		content.Version.Message = time.Now().Local().Format("机器人更新于2006-01-02 15:04:05")
		content.SetStorageBody(data)

		//设置父页面
		if parentId != "" {
			content.Ancestors = []Content{
				{
					Id: parentId,
					Space: Space{
						Key: content.Space.Key,
					},
				},
			}
		}

		return cli.ContentUpdate(content)
	}
}

type Commit struct {
	CommitId   string
	Href       string
	CommitInfo string
}

type DrawModifyPageOption struct {
	Space               string   // 空间
	Title               string   // 标题
	ParentId            string   // 父亲节点
	ConfluenceDirPrefix string   // 在confluence上的目录前缀
	Data                string   // 文件内容
	CommitList          []Commit // 最近提交信息
	GitName             string   // 仓库名称
	GitUrl              string   // 仓库地址
	FileUrl             string   // 文件地址
	FileName            string   // 文件名称
}

//从指定空间查找或创建指定标题的内容
func (cli *Client) DrawFileWithNewNoteMacro(options *DrawModifyPageOption) (Content, error) {
	//内容中的空行会被Confluence保存时自动去掉
	//因此前先去掉，以避免对比内容变化时受到影响
	data := strings.TrimSuffix(strings.TrimPrefix(options.Data, "\n"), "\n")

	//获取当前页面的内容
	content, err := cli.ContentBySpaceAndTitle(options.Space, options.Title)
	if err != nil {
		return Content{}, fmt.Errorf("查找%s出错: %s", options.Title, err)
	}

	// 不存在则创建
	if content.Id == "" {
		noteMacro, err := GetConfluenceNoteMacro(options)
		if err != nil {
			return Content{}, err
		}
		data += noteMacro
		return cli.PageCreateInSpace(options.Space, options.ParentId, options.Title, data)
	}

	// 获取文件的路径
	pagePath := ""
	lastAncestorId := ""
	for _, ancestor := range content.Ancestors {
		pagePath += "/" + ancestor.Title
		lastAncestorId = ancestor.Id
	}
	pagePath += "/" + content.Title

	//存在，但不在指定的路径下，报错结束
	if !strings.HasPrefix(pagePath, options.ConfluenceDirPrefix) {
		return Content{}, fmt.Errorf("一个标题为 '%v' 的页面已经存在于该空间中。为您的页面输入一个不同的标题。", options.Title)
	} else {
		//存在：对比内容是否有变化
		newValue, err := cli.ContentBodyConvertTo(data, "storage", "view")
		if err != nil {
			return Content{}, fmt.Errorf("转换新内容失败: %s", err)
		}

		oldValue, err := cli.ContentBodyConvertTo(content.Body.Storage.Value, "storage", "view")
		if err != nil {
			return Content{}, fmt.Errorf("转换旧内容失败: %s", err)
		}

		// 去除原文件的备注宏
		if len(strings.Split(oldValue, ConfluenceNoteSplite)) > 1 {
			oldValue = strings.Split(oldValue, ConfluenceNoteSplite)[0]
		}

		if newValue == oldValue && lastAncestorId == options.ParentId {
			return content, nil
		} else {
			// 如果确定要更新confluence页面，那么这里添加一个备注宏
			noteMacro, err := GetConfluenceNoteMacro(options)
			if err != nil {
				return Content{}, err
			}
			data += noteMacro
		}

		// 存在则否则更新
		content.Space.Key = options.Space
		content.Version.Number += 1
		content.Version.Message = time.Now().Local().Format("机器人更新于2006-01-02 15:04:05")
		content.SetStorageBody(data)

		//设置父页面
		if options.ParentId != "" {
			content.Ancestors = []Content{
				{
					Id: options.ParentId,
					Space: Space{
						Key: content.Space.Key,
					},
				},
			}
		}

		return cli.ContentUpdate(content)
	}
}

func GetConfluenceNoteMacro(options *DrawModifyPageOption) (string, error) {
	tpl := template.Must(template.New("Note Macro").Parse(NewConfluenceNoteMacro))
	outPut := bytes.Buffer{}
	err := tpl.Execute(&outPut, options)
	if err != nil {
		return "", err
	}
	return outPut.String(), nil
}
