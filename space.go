package confluence

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/russross/blackfriday"
)

type RepresentationValue struct {
	Representation string
	Value          string
}

type SpaceDescription struct {
	Plain RepresentationValue
	View  RepresentationValue
}

type SpaceLabel struct {
	Prefix string
	Name   string
	Id     string
}

type SpaceMetadata struct {
	Labels struct {
		PageResp
		Results []SpaceLabel
	}
}

type Space struct {
	Id          int                 `json:"id,omitempty"`
	Key         string              `json:"key,omitempty"`
	Name        string              `json:"name,omitempty"`
	Type        string              `json:"type,omitempty"`
	Icon        *Icon               `json:"icon,omitempty"`
	Description *SpaceDescription   `json:"description,omitempty"`
	HomePage    *Content            `json:"homePage,omitempty"`
	Metadata    *SpaceMetadata      `json:"metadata,omitempty"`
	Links       *LinkResp           `json:"_links,omitempty"`
	Expandable  *ExpandableResponse `json:"_expandable,omitempty"`
}

func (cli *Client) SpaceByKey(key string) (Space, error) {
	resp, err := cli.GET("/space/"+key, nil)
	if err != nil {
		return Space{}, fmt.Errorf("执行请求失败: %s", err)
	}

	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Space{}, fmt.Errorf("[%d]%s", resp.StatusCode, resp.Status)
	}

	var info Space
	err = json.NewDecoder(resp.Body).Decode(&info)
	if err != nil {
		return Space{}, fmt.Errorf("解析响应失败: %s", err)
	}

	return info, nil
}
func (cli *Client) SpaceContentExportToPath(key, outDir string) error {
	resp, err := cli.GET("/space/"+key+"/content", url.Values{"expand": {"body.storage,ancestors"}})
	if err != nil {
		return fmt.Errorf("执行请求失败: %s", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("[%d]%s", resp.StatusCode, resp.Status)
	}

	var info struct {
		Page struct {
			PageResp
			LinkResp
			Results []Content
		}
		BlogPost struct {
			PageResp
			LinkResp
			Results []Content
		}
		LinkResp
	}

	err = json.NewDecoder(resp.Body).Decode(&info)
	if err != nil {
		return fmt.Errorf("解析响应失败: %s", err)
	}

	//清空原目录
	os.RemoveAll(outDir)

	pageOutDir := path.Join(outDir, "page")
	os.MkdirAll(pageOutDir, 0755)
	for i, page := range info.Page.Results {
		fmt.Printf("[%3d/%3d] %s(%d Bytes)", i+1, len(info.Page.Results), page.Title, len(page.Body.Storage.Value))

		//获取父页面信息
		ancestorDirs := make([]string, len(page.Ancestors))
		for i, ancestor := range page.Ancestors {
			ancestorDirs[i] = ancestor.Title
		}

		pageDirs := append([]string{pageOutDir}, ancestorDirs...)

		pageDir := path.Join(pageDirs...)
		os.MkdirAll(pageDir, 0755)

		file := path.Join(pageDir, page.Title+".xml")

		fmt.Println("       =>", file)
		ioutil.WriteFile(file, []byte(page.Body.Storage.Value), 0755)
	}

	postOutDir := path.Join(outDir, "post")
	os.MkdirAll(postOutDir, 0755)
	for i, post := range info.BlogPost.Results {
		fmt.Printf("[%3d/%3d] %s(%d Bytes)", i+1, len(info.BlogPost.Results), post.Title, len(post.Body.Storage.Value))

		//获取父页面信息
		ancestorDirs := make([]string, len(post.Ancestors))
		for i, ancestor := range post.Ancestors {
			ancestorDirs[i] = ancestor.Title
		}

		postDirs := append([]string{postOutDir}, ancestorDirs...)

		postDir := path.Join(postDirs...)
		os.MkdirAll(postDir, 0755)

		file := path.Join(postDir, post.Title+".xml")

		fmt.Println("       =>", file)
		ioutil.WriteFile(file, []byte(post.Body.Storage.Value), 0755)
	}

	return nil
}

var supportedFileExts = []string{".md", ".xml"}
var DefaultDirContentData = []byte(`<ac:structured-macro ac:name="children"><ac:parameter ac:name="all">true</ac:parameter></ac:structured-macro>`)

func getDirContentData(dir string) ([]byte, error) {
	//检查是否有索引文件，如果有则用索引替换掉缺省的标准模板
	for _, ext := range supportedFileExts {
		indexFile := filepath.Join(dir, "index"+ext)
		buff, err := ioutil.ReadFile(indexFile)
		if err == nil {
			return buff, nil
		}

		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("读取失败: %s", err)
		}
	}

	//如果没有找到任何合适的索引文件，就用缺省模板
	return DefaultDirContentData, nil
}

//Confluence的目录宏，用于自动添加到编译后的页面
const ConfluenceToc = `
<ac:structured-macro ac:name="toc">
	<ac:parameter ac:name="outline">true</ac:parameter>
</ac:structured-macro>
`

func getFileContentData(file, ext string) ([]byte, error) {
	if ext == ".xml" {
		return ioutil.ReadFile(file)
	}

	if ext == ".md" {
		rawData, err := ioutil.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("读取错误: %s", err)
		}

		mdData := blackfriday.Run(rawData)

		return append([]byte(ConfluenceToc), mdData...), nil
	}

	return nil, fmt.Errorf("不支持的文件格式: %s", ext)
}

func (cli *Client) SpaceContentImportFrom(space, fromPath string) error {
	_, err := os.Stat(fromPath)
	if err != nil {
		return fmt.Errorf("读取目录失败: %s", err)
	}

	dirs, files, err := getContentInfoLists(fromPath)
	if err != nil {
		return fmt.Errorf("获取列表错误: %s", err)
	}

	//缓存已经创建的Content ID，以便其子Content查找父Content的ID
	contentIds := make(map[string]string)

	//处理目录
	for _, item := range dirs {
		log.Printf("目录: %+v", item)

		data, err := getDirContentData(item.Path)
		if err != nil {
			return fmt.Errorf("处理目录%s失败: %s", item.Path, err)
		}

		parentId := contentIds[item.ParentTitle]
		content, err := cli.PageFindOrCreateBySpaceAndTitle(space, parentId, item.Title, string(data))
		if err != nil {
			return fmt.Errorf("创建/更新%s错误: %s", item.Path, err)
		}

		contentIds[item.Title] = content.Id
	}

	//处理文件
	for _, item := range files {
		log.Printf("文件: %+v", item)

		buff, err := getFileContentData(item.Path, item.Ext)
		if err != nil {
			return fmt.Errorf("处理文件%s失败: %s", item.Path, err)
		}

		parentId := contentIds[item.ParentTitle]
		_, err = cli.PageFindOrCreateBySpaceAndTitle(space, parentId, item.Title, string(buff))
		if err != nil {
			return fmt.Errorf("创建/更新%s错误: %s", item.Path, err)
		}
	}

	return nil
}

// 从指定目录获取有效Conten列表
func getContentInfoLists(rootPath string) ([]FileContentInfo, []FileContentInfo, error) {
	absRootPath, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, nil, fmt.Errorf("获取%s的绝对路径失败: %s", rootPath, err)
	}

	dirs := make([]FileContentInfo, 0)
	files := make([]FileContentInfo, 0)
	titles := make(map[string][]string)

	err = filepath.Walk(absRootPath, func(path string, info os.FileInfo, err error) error {
		//遍历的filepath和rootPath取相对路径肯定是始终成功的
		relPath, _ := filepath.Rel(absRootPath, path)

		//顶层目录自身不需处理
		if relPath == "." {
			return nil
		}

		contentInfo := GetFileContentInfo(relPath)
		contentInfo.Path = path

		title := contentInfo.Title

		//以.开头的文件跳过、以.开头的目录及其子目录跳过
		if title == "" && contentInfo.Ext != "" {
			if info.IsDir() {
				return filepath.SkipDir
			} else {
				return nil
			}
		}

		//目录直接处理
		if info.IsDir() {
			dirs = append(dirs, contentInfo)

			if _, found := titles[title]; !found {
				titles[title] = make([]string, 0, 1)
			}
			titles[title] = append(titles[title], path)

			return nil
		}

		//只支持普通文件，不支持符号链接、设备等其他类型的文件
		if !info.Mode().IsRegular() {
			return fmt.Errorf("文件%s不是普通文件", path)
		}

		//目前只支持md、xml格式的文件
		var isExtSupport bool
		for _, ext := range supportedFileExts {
			if ext == contentInfo.Ext {
				isExtSupport = true
			}
		}
		if !isExtSupport {
			return nil
		}

		//索引文件会在目录列表处理时读取，文件列表直接忽略
		if title == "index" {
			return nil
		}

		files = append(files, contentInfo)

		if _, found := titles[title]; !found {
			titles[title] = make([]string, 0, 1)
		}
		titles[title] = append(titles[title], path)

		return nil
	})

	if err != nil {
		return nil, nil, fmt.Errorf("遍历目录%s错误: %s", rootPath, err)
	}

	var duplicatedCount int
	for title, matches := range titles {
		if len(matches) == 1 {
			continue
		}
		duplicatedCount += 1
		log.Println(title, "x", len(matches))
		for _, match := range matches {
			log.Println("\t", match)
		}
	}

	if duplicatedCount > 0 {
		return nil, nil, fmt.Errorf("有%d个重复的标题", duplicatedCount)
	}

	return dirs, files, nil
}

type FileContentInfo struct {
	Path        string
	Title       string
	ParentTitle string
	Ext         string
}

// 获取指定文件的信息
func GetFileContentInfo(path string) FileContentInfo {
	filename := filepath.Base(path)

	ext := filepath.Ext(filename)
	title := strings.TrimSuffix(filename, ext)

	parentTitle := filepath.Base(filepath.Dir(path))
	if parentTitle == "." {
		parentTitle = ""
	}

	return FileContentInfo{
		Ext:         ext,
		Title:       title,
		ParentTitle: parentTitle,
	}
}