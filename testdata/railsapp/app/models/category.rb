class Category < ApplicationRecord
  self.table_name = "taxonomy"
  belongs_to :parent, class_name: "Category", optional: true

  def refresh_counters!
    Post.where(category_id: id).count
  end
end
